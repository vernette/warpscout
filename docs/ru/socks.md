# SOCKS5-прокси

[← Назад к README](../../README_RU.md)

> [!WARNING]
> Команда для проверки эндпоинта, а не для постоянного использования. `socks` поднимает один туннель и держит его: переподключения и запасного эндпоинта нет, так что стоит туннелю оборваться, и прокси перестанет работать. Смену сети процесс тоже не переживёт

Всё, что показывает скан, приходит из ответа самого Cloudflare. `socks` позволяет проверить эндпоинт чем-то ещё: поднимает соединение к одному адресу и выставляет его SOCKS5-прокси на localhost, так что `curl`, браузер или скрипт вроде [ipregion](https://github.com/vernette/ipregion) (определение региона) и [censorcheck](https://github.com/vernette/censorcheck) (проверка блокировок) ходят именно через этот эндпоинт - без установки VPN-клиента и без прав администратора.

```sh
warpscout socks -e 188.114.99.218:2408 -p awg
```

```
For testing purposes ONLY.
One tunnel, no reconnect, no failover. For everyday use take a "scan -conf" config into a real client.

╭───────────────────────────────────╮
│ SOCKS5   socks5h://127.0.0.1:1080 │
│ Endpoint 188.114.99.218:2408      │
│ Tunnel   awg                      │
│ Exit     Moscow, RU (DME node)    │
╰───────────────────────────────────╯

Exit is what speed.cloudflare.com reports. Confirm it elsewhere:
  curl -x socks5h://127.0.0.1:1080 https://ifconfig.co/json

Point clients at socks5h://, not socks5:// - the name has to be resolved in the tunnel.

Ctrl+C to stop the proxy
```

`Exit` - мнение самого Cloudflare о том, куда выходит туннель. Прокси позволяет направить в него сторонний сервис и посмотреть, совпадает ли он с этим мнением.

`-e/-endpoint` принимает ровно то, что печатает `scan -best`, так что команды соединяются в пайп:

```sh
warpscout socks -e "$(warpscout scan -p awg -best)" -p awg
```

`-P/-port` меняет порт, на котором слушает прокси (по умолчанию 1080), `-l/-listen` - адрес (`127.0.0.1`). Флаги протокола и маскировки те же, что у скана: `-p wg|awg|masque|masque-h2`, `-gen-i1`, `-masque-sni` и остальные.

```sh
warpscout socks -e 162.159.198.1:443 -p masque -masque-sni www.apple.com -port 9050
```

> [!IMPORTANT]
> Направлять клиенты нужно через `socks5h://`, а не `socks5://`: имя хоста должно резолвиться на стороне туннеля, а не на стороне клиента. Иначе часть сервисов не откроется вовсе

```sh
curl -x socks5h://127.0.0.1:1080 https://ifconfig.co/json
```

`-through` здесь тоже работает, и это единственный способ попробовать [вложенный туннель](warp-in-warp.md), не собирая цепочку интерфейсов руками:

```sh
warpscout socks -e 8.47.69.130:2408 -p awg -through 188.114.97.177:2408
# Tunnel   wg through 188.114.97.177:2408 (awg)
# Exit     Frankfurt-am-Main, DE (FRA node)
curl -x socks5h://127.0.0.1:1080 https://ifconfig.co/json
# "ip": "104.28.197.9", "country": "Germany", "city": "Frankfurt am Main"
```

`Ctrl+C` останавливает прокси и туннель.
