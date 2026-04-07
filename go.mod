module github.com/rigerc/go-navidrome-ratings-sync

go 1.26.1

require (
	github.com/bogem/id3v2 v1.2.0
	github.com/knadh/koanf/parsers/yaml v1.1.0
	github.com/knadh/koanf/providers/env v1.1.0
	github.com/knadh/koanf/providers/file v1.2.1
	github.com/knadh/koanf/v2 v2.3.4
	github.com/mewkiz/flac v1.0.13
	github.com/spf13/cobra v1.10.2
	github.com/supersonic-app/go-subsonic v0.0.0
)

replace github.com/supersonic-app/go-subsonic => ./docs/go-subsonic

require (
	github.com/fsnotify/fsnotify v1.9.0 // indirect
	github.com/go-viper/mapstructure/v2 v2.4.0 // indirect
	github.com/icza/bitio v1.1.0 // indirect
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/knadh/koanf/maps v0.1.2 // indirect
	github.com/mewkiz/pkg v0.0.0-20250417130911-3f050ff8c56d // indirect
	github.com/mewpkg/term v0.0.0-20241026122259-37a80af23985 // indirect
	github.com/mitchellh/copystructure v1.2.0 // indirect
	github.com/mitchellh/reflectwalk v1.0.2 // indirect
	github.com/spf13/pflag v1.0.9 // indirect
	go.yaml.in/yaml/v3 v3.0.4 // indirect
	golang.org/x/image v0.23.0 // indirect
	golang.org/x/sys v0.32.0 // indirect
	golang.org/x/text v0.21.0 // indirect
)
