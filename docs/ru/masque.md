# MASQUE

[← Назад к README](../../README_RU.md)

Cloudflare раздаёт WARP не только по WireGuard, но и по MASQUE. Это отдельный транспорт со своими адресами, и он бывает двух видов:

- `-p masque` - поверх QUIC (UDP)
- `-p masque-h2` - то же самое поверх TCP, снаружи выглядит как обычный HTTPS

```sh
warpscout scan -p masque
warpscout scan -p masque-h2 -masque-sni www.apple.com
```

В фильтрующей сети смысл имеет в первую очередь `masque-h2`: у него настоящие пулы адресов, и TCP реже режут.

## Чем MASQUE отличается от WireGuard

- **Свои адреса.** У `masque` пул крошечный и фиксированный: два anycast-адреса на блок (`162.159.198.1` и `.2` по IPv4, `2606:4700:103::1`, `::2` и `2606:4700:104::1`, `::2` по IPv6) на фиксированном наборе портов, так что сэмплировать флагам `-n`/`-f` тут нечего. У `masque-h2` отвечают целиком `162.159.198.0/24` и `162.159.199.0/24`, а под `-6` - `2606:4700:103::/48` и `2606:4700:104::/48`, и `-n`/`-f` работают как обычно.
- **Одна нода на прогон.** Все эндпоинты одного запуска выходят через одну и ту же ноду, и зависит она от вашей сети, а не от выбранного адреса. Поэтому `-node` и `-country` под MASQUE отвергаются.
- **Эндпоинты нестабильны**, поэтому каждый проверяется минимум 3 раза. Меняется флагом `-masque-attempts N`.
- **Своё устройство.** `register` заводит MASQUE-аккаунт рядом с аккаунтом для WireGuard/AmneziaWG, потому что одно устройство не может быть и тем и другим. Один и тот же аккаунт обслуживает `masque` и `masque-h2`.

## SNI

У MASQUE нет ни junk-пакетов, ни `I1`. Их аналог - SNI, то есть имя хоста, которое видно снаружи при установке соединения:

```sh
warpscout scan -p masque -masque-sni www.apple.com
```

По умолчанию используется `consumer-masque.cloudflareclient.com`, и в фильтрующей сети он часто не проходит. Подобрать рабочий поможет `find-sni`:

```sh
warpscout find-sni
```

Команда переберёт список имён и напечатает готовую команду скана с лучшим из них. Перебор останавливается, как только какой-то SNI поднимает долю эндпоинтов из `-threshold` (по умолчанию 70%), `Ctrl+C` сохраняет лучший найденный.

Между двумя транспортами SNI не переносится - работающий поверх QUIC может быть мёртвым поверх TCP, - поэтому `-p` выбирает, для какого из них искать:

```sh
warpscout find-sni -p masque-h2
# warpscout scan -proto masque-h2 -masque-sni www.apple.com
```

## Конфиги

`-conf` пишет `config.json` для [usque](https://github.com/Diniboy1123/usque), а не `.conf`, и печатает команду запуска:

```sh
warpscout scan -p masque -conf usque.json
# usque socks -c usque.json -P 8443 -s www.apple.com

warpscout scan -p masque-h2 -conf usque.json
# usque socks -c usque.json -P 1701 -s www.apple.com --http2
```

`-conf-type mihomo` работает для обоих режимов: TCP-вариант - это тот же `type: masque` с `network: h2`.

`-table-off`, `-mtu` и `-dns`/`-no-dns` не имеют аналога в `config.json` usque и здесь игнорируются. Под `-conf-type mihomo` флаги DNS работают как обычно.
