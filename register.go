package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"strings"
	"time"

	"golang.org/x/crypto/curve25519"

	"github.com/amnezia-vpn/amneziawg-go/v3/tun/netstack"
	"github.com/charmbracelet/bubbles/progress"
)

const (
	regBaseURL      = "https://api.devices.cloudflare.com/v0a4005/reg"
	apiReachURL     = "https://api.devices.cloudflare.com/"
	apiHost         = "api.devices.cloudflare.com"
	cfClientVersion = "a-6.11-2223"
	cfUserAgent     = "okhttp/3.12.1"
	defaultAccount  = "warpscout-account.json"
	defaultRelay    = "https://edge-client-api.vercel.app"
	apiReachTimeout = 3 * time.Second
	registerTimeout = 15 * time.Second
	relayTimeout    = 45 * time.Second
	regTunnelMSS    = 1200
)

type account struct {
	ID            string         `json:"id"`
	Token         string         `json:"token"`
	PrivateKey    string         `json:"private_key"`
	PeerPublicKey string         `json:"peer_public_key"`
	IPv4          string         `json:"ipv4,omitempty"`
	IPv6          string         `json:"ipv6,omitempty"`
	Masque        *masqueAccount `json:"masque,omitempty"`
	Outer         *account       `json:"outer,omitempty"`
}

type masqueAccount struct {
	ID            string `json:"id"`
	Token         string `json:"token"`
	PrivateKey    string `json:"private_key"`
	PeerPublicKey string `json:"peer_public_key"`
	IPv4          string `json:"ipv4"`
	IPv6          string `json:"ipv6"`
}

func generateKeypair() (privB64, pubB64 string, err error) {
	var priv [32]byte
	if _, err = rand.Read(priv[:]); err != nil {
		return "", "", err
	}
	priv[0] &= 248
	priv[31] &= 127
	priv[31] |= 64

	pub, err := curve25519.X25519(priv[:], curve25519.Basepoint)
	if err != nil {
		return "", "", err
	}
	return base64.StdEncoding.EncodeToString(priv[:]),
		base64.StdEncoding.EncodeToString(pub), nil
}

func loadAccount(path string) (account, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return account{}, err
	}
	var a account
	if err := json.Unmarshal(data, &a); err != nil {
		return account{}, err
	}
	if a.PrivateKey == "" || a.PeerPublicKey == "" {
		return account{}, fmt.Errorf("%s: incomplete account", path)
	}
	return a, nil
}

// Only a missing file means there is nothing to rotate. Every other failure -
// unreadable, malformed, incomplete - would otherwise register fresh and have
// saveAccount overwrite the file the run was asked to rotate.
func accountToRotate(path string) (account, error) {
	a, err := loadAccount(path)
	if errors.Is(err, os.ErrNotExist) {
		return account{}, nil
	}
	if err != nil {
		return account{}, fmt.Errorf("%v\nfix or move %s, or pass -fresh to replace it", err, path)
	}
	return a, nil
}

func saveAccount(path string, a account) error {
	data, err := json.MarshalIndent(a, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0600)
}

func applyAccount(a account) {
	warpPrivateKey = a.PrivateKey
	warpPublicKey = a.PeerPublicKey
	if a.IPv4 != "" {
		warpAddress = a.IPv4
	}
	if a.IPv6 != "" {
		warpAddressV6 = a.IPv6
	}
	masqueAcct = a.Masque
	outerAcct = a.Outer
}

type regResp struct {
	ID     string `json:"id"`
	Token  string `json:"token"`
	Config struct {
		Peers []struct {
			PublicKey string `json:"public_key"`
		} `json:"peers"`
		Interface struct {
			Addresses struct {
				V4 string `json:"v4"`
				V6 string `json:"v6"`
			} `json:"addresses"`
		} `json:"interface"`
	} `json:"config"`
}

func parseRegResp(body []byte, privateKey string) (account, error) {
	var r regResp
	if err := json.Unmarshal(body, &r); err != nil {
		return account{}, fmt.Errorf("parse reg response: %w", err)
	}
	if len(r.Config.Peers) == 0 || r.Config.Peers[0].PublicKey == "" {
		return account{}, fmt.Errorf("reg response missing peer public key")
	}
	a := account{
		PrivateKey:    privateKey,
		PeerPublicKey: r.Config.Peers[0].PublicKey,
		ID:            r.ID,
		Token:         r.Token,
		IPv4:          r.Config.Interface.Addresses.V4,
		IPv6:          r.Config.Interface.Addresses.V6,
	}
	return a, nil
}

func registerWARP(ctx context.Context, client *http.Client) (account, error) {
	priv, pub, err := generateKeypair()
	if err != nil {
		return account{}, err
	}

	body, err := doJSON(ctx, client, http.MethodPost, regBaseURL, "",
		map[string]string{"key": pub})
	if err != nil {
		return account{}, fmt.Errorf("POST /reg: %w", err)
	}
	a, err := parseRegResp(body, priv)
	if err != nil {
		return account{}, err
	}

	patchURL := fmt.Sprintf("%s/%s", regBaseURL, a.ID)
	if _, err := doJSON(ctx, client, http.MethodPatch, patchURL, a.Token,
		map[string]bool{"warp_enabled": true}); err != nil {
		return account{}, fmt.Errorf("PATCH /reg/%s: %w", a.ID, err)
	}
	return a, nil
}

func rotateKey(ctx context.Context, client *http.Client, existing account) (account, error) {
	priv, pub, err := generateKeypair()
	if err != nil {
		return account{}, err
	}
	body, err := doJSON(ctx, client, http.MethodPatch,
		fmt.Sprintf("%s/%s", regBaseURL, existing.ID), existing.Token,
		map[string]string{"key": pub})
	if err != nil {
		return account{}, fmt.Errorf("PATCH /reg/%s: %w", existing.ID, err)
	}
	return rotatedAccount(body, priv, existing)
}

func rotatedAccount(body []byte, priv string, existing account) (account, error) {
	a, err := parseRegResp(body, priv)
	if err != nil {
		return account{}, err
	}
	a.ID, a.Token = existing.ID, existing.Token
	return a, nil
}

func mintAccount(ctx context.Context, client *http.Client, existing account) (account, error) {
	a, err := mintWGAccount(ctx, client, existing)
	if err != nil {
		return account{}, err
	}
	a.Masque = mintMasque(ctx, client, existing.Masque)
	a.Outer = mintOuter(ctx, client, existing.Outer)
	return a, nil
}

func mintOuter(ctx context.Context, client *http.Client, existing *account) *account {
	var prev account
	if existing != nil {
		prev = *existing
	}
	o, err := mintWGAccount(ctx, client, prev)
	if err == nil {
		return &o
	}
	fmt.Fprintln(os.Stderr, errPal.fail(fmt.Sprintf("outer device registration failed (%v) - \"-through\" will not work", err)))
	return existing
}

func mintMasque(ctx context.Context, client *http.Client, existing *masqueAccount) *masqueAccount {
	m, err := registerMasque(ctx, client)
	if err == nil {
		return m
	}
	fmt.Fprintln(os.Stderr, errPal.fail(fmt.Sprintf("MASQUE device registration failed (%v) - \"-p masque\" will not work", err)))
	return existing
}

func mintWGAccount(ctx context.Context, client *http.Client, existing account) (account, error) {
	if existing.ID == "" || existing.Token == "" {
		return registerWARP(ctx, client)
	}
	a, err := rotateKey(ctx, client, existing)
	if err == nil {
		return a, nil
	}
	fmt.Fprintln(os.Stderr, errPal.fail(fmt.Sprintf("key rotation failed (%v) - registering a fresh account", err)))
	return registerWARP(ctx, client)
}

func doJSON(ctx context.Context, client *http.Client, method, urlStr, token string, payload any) ([]byte, error) {
	return doJSONAs(ctx, client, method, urlStr, token, cfUserAgent, cfClientVersion, payload)
}

func doJSONAs(ctx context.Context, client *http.Client, method, urlStr, token, userAgent, clientVersion string, payload any) ([]byte, error) {
	buf, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, method, urlStr, bytes.NewReader(buf))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("CF-Client-Version", clientVersion)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("status %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	return respBody, nil
}

func apiReachable(client *http.Client) bool {
	ctx, cancel := context.WithTimeout(context.Background(), apiReachTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiReachURL, nil)
	if err != nil {
		return false
	}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return true
}

func regTransport(proxy *url.URL) *http.Transport {
	d := &net.Dialer{}
	t := &http.Transport{DialContext: d.DialContext}
	if scanInterface != "" {
		d.Control = deviceControl(scanInterface, regTunnelMSS)
		// Force the interface's address family: a v6 dial through a v4-only tun
		// (or vice versa) blackholes, so pin the network to what the interface carries.
		network := "tcp4"
		if scanSourceIP.Is6() {
			network = "tcp6"
		}
		t.DialContext = func(ctx context.Context, _, addr string) (net.Conn, error) {
			return d.DialContext(ctx, network, addr)
		}
	}
	if proxy != nil {
		t.Proxy = http.ProxyURL(proxy)
	}
	return t
}

func proxyClient(proxyURL string) (*http.Client, error) {
	u, err := url.Parse(proxyURL)
	if err != nil {
		return nil, fmt.Errorf("invalid -proxy %q: %w", proxyURL, err)
	}
	return &http.Client{
		Timeout:   registerTimeout,
		Transport: regTransport(u),
	}, nil
}

type relayTripper struct {
	base *url.URL
	rt   http.RoundTripper
}

func (t relayTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	r := req.Clone(req.Context())
	r.URL.Scheme, r.URL.Host = t.base.Scheme, t.base.Host
	r.URL.Path = strings.TrimSuffix(t.base.Path, "/") + req.URL.Path
	r.Host = ""
	// The relay runs on fetch(), which gunzips the body but forwards the
	// Content-Encoding header as it stands - so a transport that asked for gzip
	// then tries to inflate plain JSON and dies with "gzip: invalid header".
	r.Header.Set("Accept-Encoding", "identity")
	return t.rt.RoundTrip(r)
}

const relayOff = "none"

func applyRelay(o *options) error {
	if o.relay == relayOff {
		o.relay = ""
		return nil
	}
	_, err := parseRelayURL(o.relay)
	return err
}

func relayClient(relayURL string) (*http.Client, error) {
	u, err := parseRelayURL(relayURL)
	if err != nil {
		return nil, err
	}
	return &http.Client{
		Timeout:   relayTimeout,
		Transport: relayTripper{base: u, rt: regTransport(nil)},
	}, nil
}

func parseRelayURL(relayURL string) (*url.URL, error) {
	u, err := url.Parse(relayURL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, fmt.Errorf("invalid -relay %q: want an http(s) URL (or \"none\" to disable)", relayURL)
	}
	return u, nil
}

func resolveIPv4(host string) (string, error) {
	ips, err := net.LookupHost(host)
	if err != nil {
		return "", err
	}
	for _, ip := range ips {
		if a, err := netip.ParseAddr(ip); err == nil && a.Is4() {
			return ip, nil
		}
	}
	return "", fmt.Errorf("no IPv4 address for %s", host)
}

func tunnelClient(tnet *netstack.Net) (*http.Client, error) {
	// The netstack tunnel only has a v4 address (warpAddress), so the API must be
	// dialed over IPv4 - a v6 target picked from LookupHost would blackhole.
	ip, err := resolveIPv4(apiHost)
	if err != nil {
		return nil, fmt.Errorf("resolve %s: %w", apiHost, err)
	}
	target := net.JoinHostPort(ip, "443")
	return &http.Client{
		Timeout: registerTimeout,
		Transport: &http.Transport{
			DisableKeepAlives: true,
			DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
				return tnet.DialContext(ctx, "tcp", target)
			},
		},
	}, nil
}

const registerSample = 5

const tunnelDiscoverySample = 64

const tunnelDiscoveryBudget = 40 * time.Second

func obtainAccount(ctx context.Context, o options, ips []netip.Addr, timeout time.Duration, existing account) (account, error) {
	if o.proxy != "" {
		c, err := proxyClient(o.proxy)
		if err != nil {
			return account{}, err
		}
		return mintAccount(ctx, c, existing)
	}

	fmt.Fprintln(os.Stderr, errPal.dim("Checking Cloudflare API availability..."))
	direct := &http.Client{Timeout: registerTimeout, Transport: regTransport(nil)}
	if apiReachable(direct) {
		return mintAccount(ctx, direct, existing)
	}

	fmt.Fprintf(os.Stderr, "\n%s\n\n", errPal.fail("API unreachable directly"))

	if o.relay != "" {
		fmt.Fprintln(os.Stderr, errPal.dim(fmt.Sprintf("Registering through the relay (%s)...", o.relay)))
		c, err := relayClient(o.relay)
		if err != nil {
			return account{}, err
		}
		a, err := mintAccount(ctx, c, existing)
		if err == nil {
			return a, nil
		}
		fmt.Fprintln(os.Stderr, errPal.fail(fmt.Sprintf("  relay: %v", err)))
	}

	fmt.Fprintln(os.Stderr, errPal.dim("Registering through a WARP tunnel (pass -proxy to use a proxy instead)"))

	sampled := sampleAddrs(ips, tunnelDiscoverySample)
	origI1, origLabel := awgI1, genI1Label
	var lastErr error
	for _, p := range []protoRun{{kindAWG, protoAWG}, {kindWG, protoWG}} {
		for _, c := range regI1Candidates(p, o, origI1, origLabel) {
			// newTunnel bakes the globals into the UAPI config, so set them first.
			awgI1, genI1Label = c.chain, c.label
			label := p.name
			if p.isAWG() && !o.i1Explicit {
				label += fmt.Sprintf(" (%s)", i1NoteFor(c.chain, c.label))
			}
			onProbe := discoveryProgress(label, len(sampled), usePlainOutput(o))
			a, err := registerViaTunnel(ctx, p, sampled, timeout, onProbe, existing)
			if onProbe != nil {
				fmt.Fprintln(os.Stderr)
			}
			if err == nil {
				reportRegI1(origI1)
				return a, nil
			}
			fmt.Fprintln(os.Stderr, errPal.fail(fmt.Sprintf("  %s: %v", label, err)))
			lastErr = err
		}
	}
	awgI1, genI1Label = origI1, origLabel
	return account{}, lastErr
}

type i1Candidate struct{ chain, label string }

// Only awg carries an I1, and an explicit -i1/-gen-i1 is the user's choice - the
// fallback sweep only replaces the default probe when DPI drops it.
func regI1Candidates(run protoRun, o options, curChain, curLabel string) []i1Candidate {
	if !run.isAWG() || o.i1Explicit {
		return []i1Candidate{{curChain, curLabel}}
	}
	cands := []i1Candidate{{i1Default, ""}}
	for _, p := range i1Profiles() {
		if chain, label, err := genI1(p, o.i1Host); err == nil {
			cands = append(cands, i1Candidate{chain, label})
		}
	}
	return cands
}

func reportRegI1(origI1 string) {
	if awgI1 == origI1 {
		return
	}
	fmt.Fprintln(os.Stderr, errPal.ok(fmt.Sprintf("Registered with the %s - reusing it for this run", i1Note())))
	fmt.Fprintln(os.Stderr, errPal.dim(fmt.Sprintf("  reuse it later with: -i1 %q", awgI1)))
}

const discoveryBarWidth = 28

// A redrawing bar needs a terminal, so plain mode prints one static line and
// returns no callback.
func discoveryProgress(what string, total int, plain bool) func(probed int) {
	label := fmt.Sprintf("probing %s endpoints", what)
	if plain {
		fmt.Fprintf(os.Stderr, "  %s...\n", label)
		return nil
	}
	bar := progress.New(progress.WithDefaultGradient(), progress.WithWidth(discoveryBarWidth))
	return func(probed int) {
		fmt.Fprintf(os.Stderr, "\r\033[K  %s %s", label, bar.ViewAs(float64(probed)/float64(total)))
	}
}

// The handshake is the only reachability test that survives the DPI which
// forced the tunnel fallback in the first place.
func registerViaTunnel(ctx context.Context, run protoRun, ips []netip.Addr, timeout time.Duration, onProbe func(probed int), existing account) (account, error) {
	tn, err := newTunnel(run)
	if err != nil {
		return account{}, err
	}
	defer tn.Close()

	ctx, cancel := context.WithTimeout(ctx, tunnelDiscoveryBudget)
	defer cancel()
	for i, ip := range ips {
		if ctx.Err() != nil {
			break
		}
		if onProbe != nil {
			onProbe(i + 1)
		}
		if !tunnelConnect(ctx, tn, ip, timeout) {
			continue
		}
		client, err := tunnelClient(tn.stack().tnet)
		if err != nil {
			return account{}, err
		}
		return mintAccount(ctx, client, existing)
	}
	return account{}, fmt.Errorf("no reachable endpoint")
}
