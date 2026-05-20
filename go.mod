module bearlogin-bridge

go 1.24.0

require (
	github.com/go-telegram/bot v1.20.0
	github.com/golang-migrate/migrate/v4 v4.19.1
	github.com/lib/pq v1.11.2
	github.com/mattn/go-sqlite3 v1.14.34
	github.com/max-messenger/max-bot-api-client-go v1.4.2
)

require (
	github.com/caarlos0/env/v6 v6.10.1 // indirect
	github.com/mattn/go-colorable v0.1.13 // indirect
	github.com/mattn/go-isatty v0.0.19 // indirect
	github.com/rs/zerolog v1.34.0 // indirect
	golang.org/x/sys v0.38.0 // indirect
	gopkg.in/yaml.v2 v2.4.0 // indirect
)

replace github.com/go-telegram/bot => github.com/Flipped199/telegram-bot v1.20.0-fix.1
