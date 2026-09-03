# Docker

[← Назад к README](../../README_RU.md)

Образ мультиархитектурный (`linux/amd64` и `linux/arm64`). Рабочая директория контейнера - `/data`, туда же кладётся файл аккаунта.

```sh
# Регистрация аккаунта WARP
docker run --rm -it --user "$(id -u):$(id -g)" -v "$PWD:/data" vernette/warpscout register

# Скан по AmneziaWG
docker run --pull always --rm -it --user "$(id -u):$(id -g)" -v "$PWD:/data" vernette/warpscout scan -p awg
```

## Сохранить аккаунт между запусками

Примонтируйте директорию (`-v "$PWD:/data"`), иначе аккаунт умрёт вместе с контейнером и регистрироваться придётся каждый раз.

> [!NOTE]
> Контейнер работает от root, поэтому без `--user` всё записанное в примонтированную директорию - файл аккаунта, отчёт, конфиг `-conf` - принадлежит root, а не текущему пользователю. `$(id -u)` - синтаксис шелла Linux и macOS. В Docker Desktop для Windows флаг не нужен: драйвер файловой системы сопоставляет владельца сам

## Цвет и живая панель

И то, и другое включается, только когда вывод идёт в терминал, поэтому нужен `-it`: `-t` выделяет псевдотерминал, `-i` подключает стандартный ввод. Без `-i` ответы терминала на запросы панели никто не читает, и они утекают в шелл сырыми символами.

## Пинг внутри контейнера

На современном Docker делать ничего не надо. Если `ENDPOINT PING` показывает `?` (старый Docker, ужесточённые настройки, другой движок контейнеров), добавьте sysctl:

```sh
docker run --rm -it --sysctl net.ipv4.ping_group_range="0 2147483647" \
  -v "$PWD:/data" vernette/warpscout scan -p awg
```

`TUN PING` работает внутри туннеля и не требует привилегий ни в каком контейнере.

## IPv6 и выбор интерфейса

`-6` и `-I` требуют сети хоста. У контейнера своё сетевое пространство имён, интерфейсов хоста там нет, а IPv6 обычно выключен:

```sh
docker run --rm -it --network host -v "$PWD:/data" vernette/warpscout scan -p awg -6
docker run --rm -it --network host -v "$PWD:/data" vernette/warpscout scan -p awg -I eth0
```

## Собрать образ

```sh
# под свою систему
docker build -t warpscout .

# под другую платформу
docker buildx build --platform linux/arm64 -t vernette/warpscout:arm --load .
```
