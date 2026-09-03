<h1 align="center">WARPSCOUT</h1>

![WARPSCOUT multi-node](.github/assets/warpscout-multi-node.png)

<p align="center">Поиск эндпоинтов Cloudflare WARP, которые работают из вашей сети, и проверка того, где они выходят.</p>

<p align="center">
  <a href="https://github.com/vernette/warpscout/releases"><img src="https://img.shields.io/github/release/vernette/warpscout.svg" alt="GitHub Release"></a>
  <a href="https://github.com/vernette/warpscout/actions/workflows/release.yaml"><img src="https://img.shields.io/github/actions/workflow/status/vernette/warpscout/release.yaml" alt="Build Status"></a>
  <a href="https://github.com/vernette/warpscout/actions/workflows/test.yaml"><img src="https://img.shields.io/github/actions/workflow/status/vernette/warpscout/test.yaml?label=tests" alt="Tests"></a>
  <a href="https://github.com/vernette/warpscout/releases"><img src="https://img.shields.io/github/downloads/vernette/warpscout/total" alt="GitHub Downloads"></a>
  <a href="https://hub.docker.com/r/vernette/warpscout"><img src="https://img.shields.io/docker/pulls/vernette/warpscout?logo=docker" alt="Docker Pulls"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-MIT-blue" alt="License: MIT"></a>
</p>

<p align="center">Документация: 🇷🇺 Русский &middot; <a href="README.md">🇬🇧 English</a></p>

## Содержание

- [Что это](#что-это)
- [Установка](#установка)
- [Быстрый старт](#быстрый-старт)
- [Как читать таблицу](#как-читать-таблицу)
- [Если ничего не нашлось](#если-ничего-не-нашлось)
- [Команды](#команды)
- [Полезные флаги](#полезные-флаги)
- [Что дальше](#что-дальше)
- [Благодарности](#благодарности)
- [Поддержать автора](#поддержать-автора)

## Что это

У Cloudflare WARP тысячи адресов эндпоинтов, и от того, к какому из них вы подключились, зависит, на какой пограничный узел (ноду) Cloudflare попадёт туннель. Это важно: с апреля 2026 года трафик через московскую ноду `DME` фильтруется DPI, часть сайтов через неё просто не открывается, хотя сам WARP подключается нормально. Тот же конфиг, направленный на эндпоинт с зарубежной нодой, работает без этой проблемы.

В официальном клиенте WARP выбрать ноду нельзя. WARPSCOUT перебирает адреса эндпоинтов, показывает для каждого ноду и регион выхода, и отдаёт готовый конфиг для лучшего из них.

- Один файл, ничего устанавливать не нужно
- Без прав администратора и без TUN-устройства
- Windows, Linux, macOS, Android и Docker
- Живая таблица во время скана и файл отчёта на диск

## Установка

### Windows

1. Откройте [страницу релизов](https://github.com/vernette/warpscout/releases) и скачайте `warpscout_..._windows_amd64.zip`.
2. Распакуйте архив в любую папку, например `C:\warpscout`.
3. Откройте эту папку в проводнике, зажмите **Shift** и нажмите правой кнопкой мыши по пустому месту → **Открыть окно PowerShell здесь** (в Windows 11 - **Открыть в терминале**).
4. Проверьте, что всё работает:

```powershell
.\warpscout.exe version
```

Дальше все команды из этого README вводятся в это же окно.

> [!NOTE]
> Если Windows ругается на скачанный файл, нажмите правой кнопкой по `warpscout.exe` → Свойства → внизу галочка **Разблокировать**

### Android

Проще всего - готовое приложение [openwarpkit/warpscout-android](https://github.com/openwarpkit/warpscout-android) от [OpenWarpKit](https://github.com/openwarpkit). APK под свою архитектуру берите на [странице релизов](https://github.com/openwarpkit/warpscout-android/releases).

В Termux работает установка одной командой, как для Linux ниже.

### Linux, macOS, OpenWrt, Termux

```sh
curl -fsSL https://raw.githubusercontent.com/vernette/warpscout/master/install.sh | sh
```

Если `curl` нет (например, на OpenWrt):

```sh
wget -qO- https://raw.githubusercontent.com/vernette/warpscout/master/install.sh | sh
```

Скрипт подберёт архив под вашу систему и положит `warpscout` в `~/.local/bin`, на OpenWrt - в `/usr/bin`, в Termux - в `$PREFIX/bin`. Для обновления просто запустите его ещё раз.

> [!NOTE]
> **Только для macOS.** Первый запуск блокируется, потому что файл скачан из интернета. Снимите этот флаг один раз: `xattr -d com.apple.quarantine warpscout`

### Другие способы

Arch Linux (AUR, пакет собрал [Nebulosa](https://aur.archlinux.org/account/Nebulosa), этот репозиторий его не сопровождает):

```sh
paru -S warpscout      # сборка из исходников
paru -S warpscout-bin  # готовый бинарник из релиза
```

Docker - см. [запуск в Docker](docs/ru/docker.md).

Сборка из исходников, Go 1.25 или новее:

```sh
go install github.com/vernette/warpscout@latest
```

Опции установщика передаются после `sh -s --`, а каталог выбирает `INSTALL_DIR`:

```sh
INSTALL_DIR=/usr/local/bin sh -c "$(curl -fsSL https://raw.githubusercontent.com/vernette/warpscout/master/install.sh)"

curl -fsSL https://raw.githubusercontent.com/vernette/warpscout/master/install.sh | sh -s -- --version v0.8.1
curl -fsSL https://raw.githubusercontent.com/vernette/warpscout/master/install.sh | sh -s -- --uninstall
```

Если нужен конкретный архив вручную, вот таблица соответствия:

| Ваша ОС                                | Файл                   |
| -------------------------------------- | ---------------------- |
| Windows                                | `windows_amd64.zip`    |
| Mac на Apple silicon                   | `darwin_arm64.tar.gz`  |
| Mac на процессоре Intel                | `darwin_amd64.tar.gz`  |
| Linux, обычный ПК или сервер           | `linux_amd64.tar.gz`   |
| Linux на ARM (Raspberry Pi, часть VPS) | `linux_arm64.tar.gz`   |
| Роутер с OpenWrt, x86_64               | `linux_amd64.tar.gz`   |
| Роутер с OpenWrt, ARM64                | `linux_arm64.tar.gz`   |
| Android, в Termux                      | `android_arm64.tar.gz` |

## Быстрый старт

Примеры написаны для Windows. На Linux, macOS и в Termux вместо `.\warpscout.exe` пишите `warpscout`.

### Шаг 1. Создать аккаунт WARP

```powershell
.\warpscout.exe register
```

Команда создаст рядом с программой файл `warpscout-account.json`. Делается один раз, без него сканирование не запустится. Подробности - в [аккаунте WARP](docs/ru/account.md).

### Шаг 2. Просканировать эндпоинты

```powershell
.\warpscout.exe scan -p awg -P
```

- `-p awg` - протокол AmneziaWG, замаскированный WireGuard. В России и других сетях с фильтрацией обычный WireGuard (`-p wg`) чаще всего не проходит вообще, поэтому начинать стоит сразу с `awg`.
- `-P` - дополнительно измерить задержку и потери пакетов **внутри** туннеля. Скан идёт дольше, зато отсеиваются эндпоинты, которые подключаются, но не работают.

Скан занимает пару минут. Результат - таблица, отсортированная так, что лучший эндпоинт сверху.

![Скан WARPSCOUT с -tun-ping](.github/assets/warpscout-tun-ping.png)

### Шаг 3. Выбрать эндпоинт

Смотрите на колонку `NODE`. Подойдёт любая строка, где нода **не** `DME`, потери (`LOSS`) равны `0%`, а пинг поменьше.

Чтобы не выбирать вручную, отдайте это программе. Флаг `-exclude-node DME` выбрасывает московскую ноду, а `-best` печатает единственный лучший адрес:

```powershell
.\warpscout.exe scan -p awg -P -exclude-node DME -best
# 188.114.98.58:2408
```

Можно наоборот - оставить только нужные страны или ноды:

```powershell
.\warpscout.exe scan -p awg -P -country DE,NL -best
.\warpscout.exe scan -p awg -P -node HEL,ARN -best
```

### Шаг 4. Получить конфиг

`-conf` сразу пишет готовый конфиг для лучшего эндпоинта:

```powershell
.\warpscout.exe scan -p awg -P -exclude-node DME -conf warp.conf
```

Файл `warp.conf` импортируется в клиент [AmneziaWG](https://amnezia.org/) (Windows, Android, Linux, macOS) как обычный конфиг. Если сканировали с `-p wg`, получится обычный конфиг WireGuard.

Другие форматы (mihomo, usque), DNS, MTU и маршруты - в разделе [конфиги и фильтры](docs/ru/configs.md).

## Как читать таблицу

| Колонка         | Что означает                                                                                 |
| --------------- | -------------------------------------------------------------------------------------------- |
| `SEEN AS`       | Страна, в которой вас будут считать находящимся сайты, если пустить трафик через этот эндпоинт |
| `NODE`          | Пограничный узел Cloudflare, на который попал туннель, кодом аэропорта (`FRA`, `ARN`, `DME`)   |
| `NODE LOCATION` | Город и страна этого узла                                                                      |
| `ENDPOINT PING` | Обычный пинг до адреса эндпоинта. Показывает только то, насколько адрес далеко                 |
| `TUN PING`      | Задержка **внутри** туннеля, до `1.1.1.1`. Появляется с `-P`                                   |
| `LOSS`          | Потери пакетов внутри туннеля. Появляется с `-P`                                               |
| `SPEED`         | Скорость скачивания внутри туннеля. Появляется с `-speed`                                      |

`SEEN AS` и `NODE LOCATION` - разные вещи: туннель может идти через Франкфурт и всё равно выходить как Россия. `SEEN AS` отвечает на вопрос "какую страну увидят сайты", `NODE` - "через какой узел и какую фильтрацию идёт трафик".

Одна и та же подсеть может раздавать разные ноды, поэтому строк с одинаковым началом адреса, но разными нодами - это нормально.

Кроме таблицы на экране программа пишет файл отчёта `warpscout-report-<дата>.txt` со всеми найденными эндпоинтами. Отключается флагом `-no-report`.

## Если ничего не нашлось

Идите по списку сверху вниз, на каждом шаге запуская скан заново.

1. **Скан с `-p wg` ничего не нашёл или все эндпоинты в списке `torn down`.** Так и должно быть в фильтрующей сети. Используйте `-p awg`.

2. **`-p awg` тоже ничего не находит.** Поменяйте поддельный первый пакет, которым открывается соединение:

   ```powershell
   .\warpscout.exe scan -p awg -P -gen-i1 quic
   ```

   Вместо `quic` можно пробовать `dns`, `sip`, `stun`, `random`. Обычно помогает именно это.

3. **Не помог ни один вариант `-gen-i1`.** Запустите автоматический подбор параметров маскировки. Команда будет сканировать заново, пока не найдёт рабочий набор, и в конце напечатает готовую строку для запуска:

   ```powershell
   .\warpscout.exe find-junk -gen-i1 random
   ```

   Подробнее - в разделе [обход блокировок](docs/ru/blocking.md).

4. **AmneziaWG не проходит совсем.** Попробуйте MASQUE - второй транспорт Cloudflare, поверх TCP он выглядит как обычный HTTPS:

   ```powershell
   .\warpscout.exe find-sni -p masque-h2
   .\warpscout.exe scan -p masque-h2 -masque-sni www.apple.com
   ```

   Подробнее - в разделе [MASQUE](docs/ru/masque.md).

### Что такое `torn down`

Часть эндпоинтов попадает в отдельный список `torn down` вместо рабочих. Это эндпоинты, которые сначала подключились и передавали данные, а потом были оборваны посреди передачи - так поступает DPI с туннелем, который ему не понравился. В реальном использовании это выглядит как VPN, который подключается, работает несколько секунд и намертво зависает.

Увидеть такое можно только с флагом `-P`. Без него те же эндпоинты выглядят полностью рабочими:

![Скан wg в WARPSCOUT без -tun-ping](.github/assets/warpscout-wg-looks-working.png)

С `-P` выясняется, что рвут каждый из них:

![Скан wg в WARPSCOUT с -tun-ping](.github/assets/warpscout-wg-torn-down.png)

Разорванные эндпоинты никогда не попадают в `-best` и `-conf`. Показываются они затем, чтобы было видно, какую часть пула режет сеть - это сигнал сменить `-gen-i1`.

## Команды

| Команда     | Что делает                                                         |
| ----------- | ------------------------------------------------------------------ |
| `register`  | Создать аккаунт WARP и сохранить его. Начинать нужно с этого       |
| `scan`      | Просканировать эндпоинты и показать рабочие                        |
| `find-junk` | Подобрать настройки AmneziaWG, которые проходят через фильтр       |
| `find-sni`  | Подобрать SNI для MASQUE, который проходит через фильтр            |
| `socks`     | Отдать один эндпоинт локальным SOCKS5-прокси, чтобы проверить его  |
| `version`   | Напечатать установленную версию                                    |

## Полезные флаги

| Флаг                    | Что делает                                                                                    |
| ----------------------- | --------------------------------------------------------------------------------------------- |
| `-p, -proto`            | Протокол: `wg`, `awg`, `masque`, `masque-h2` (по умолчанию `wg`)                                |
| `-P, -tun-ping`         | Измерить задержку и потери внутри туннеля, отсеять оборванные эндпоинты                         |
| `-best`                 | Вместо таблиц напечатать один лучший адрес `ip:port`                                            |
| `-conf FILE`            | Записать готовый конфиг для лучшего эндпоинта (`-conf -` печатает его в терминал)               |
| `-node`, `-country`     | Оставить только эти ноды или страны, списком через запятую                                      |
| `-exclude-node`, `-exclude-country` | Наоборот, выбросить их. Складывается с предыдущими                                 |
| `-n, -sample N`         | Сколько адресов пробовать в каждой подсети (по умолчанию 5)                                     |
| `-f, -full`             | Пробовать все адреса каждой подсети. Долго, но тщательно                                        |
| `-speed`                | Добавить колонку `SPEED` - замер скорости скачивания. Заметно дольше                            |
| `-6, -ipv6`             | Сканировать IPv6-эндпоинты вместо IPv4                                                          |
| `-o FILE`, `-no-report` | Куда писать файл отчёта или не писать вовсе                                                     |
| `-emoji`                | Показывать флаги стран рядом с регионами                                                        |
| `-t, -timeout N`        | Таймаут на запрос в секундах (по умолчанию 2)                                                   |
| `-jt N`                 | Сколько туннелей поднимать одновременно (по умолчанию 10)                                       |

Полный список флагов любой команды - `warpscout <команда> -h`, например `.\warpscout.exe scan -h`.

## Что дальше

- [Как это работает](docs/ru/how-it-works.md) - фазы скана, чем регион отличается от ноды, проверка региона VPS
- [Аккаунт WARP](docs/ru/account.md) - что делает `register`, что лежит в файле аккаунта, регистрация из заблокированной сети
- [Обход блокировок](docs/ru/blocking.md) - маскировка AmneziaWG, `-gen-i1`, junk-пакеты, `find-junk`
- [MASQUE](docs/ru/masque.md) - `-p masque` и `-p masque-h2`, подбор SNI, конфиги usque
- [Конфиги и фильтры](docs/ru/configs.md) - форматы `-conf`, DNS и MTU, `-target`, замер скорости, формат отчёта
- [SOCKS5-прокси](docs/ru/socks.md) - проверить конкретный эндпоинт сторонними сервисами
- [WARP-in-WARP](docs/ru/warp-in-warp.md) - скан изнутри другого туннеля WARP
- [Docker](docs/ru/docker.md) - запуск в контейнере
- [Решение проблем](docs/ru/troubleshooting.md) - `ENDPOINT PING` показывает `?`, программу убивают на роутере, macOS не запускает файл

## Благодарности

- [Cloudflare](https://one.one.one.one/) - очевидно, за сам WARP
- [puzige/CloudflareWarpSpeedTest](https://github.com/puzige/CloudflareWarpSpeedTest) - запасной перебор портов и измерение RTT и потерь внутри туннеля
- [ampetelin/warp-endpoint-checker](https://github.com/ampetelin/warp-endpoint-checker) - список IPv4-подсетей WARP
- [TheyCallMeSecond/WARP-Endpoint-IP](https://github.com/TheyCallMeSecond/WARP-Endpoint-IP) - список IPv6-подсетей WARP
- [SagePtr/mini_quic_generator](https://github.com/SagePtr/mini_quic_generator) - сборка QUIC Initial-пакета, портированная для профиля I1 `quic`
- [Diniboy1123/usque](https://github.com/Diniboy1123/usque) - реализация MASQUE-клиента WARP, на которой построен `-p masque`
- [nellimonix/base-relay](https://github.com/nellimonix/base-relay) - обратный прокси, на котором работает фолбек для регистрации WARP аккаунта
- [amnezia-vpn/amneziawg-go](https://github.com/amnezia-vpn/amneziawg-go) - реализация AmneziaWG в пространстве пользователя
- [charmbracelet/bubbletea](https://github.com/charmbracelet/bubbletea) - фреймворк, на котором сделана живая панель

## Поддержать автора

<p align="center">
  <a href="https://pay.cloudtips.ru/p/327ef017"><img src="https://static.tildacdn.com/tild3465-3233-4263-b937-316135666261/Horiz.svg" height="40" alt="CloudTips"></a>
  &nbsp;&nbsp;&nbsp;
  <a href="https://nowpayments.io/donation/vernette"><img src="https://nowpayments.io/images/embeds/payments-button-white.svg" height="40" alt="NOWPayments"></a>
</p>
