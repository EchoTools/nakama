module github.com/heroiclabs/nakama/v3

go 1.26.0

replace github.com/intinig/go-openskill => github.com/thesprockee/go-openskill v0.0.0-20260410050935-54fb95d3a264

require (
	buf.build/gen/go/echotools/nevr-api/protocolbuffers/go v1.36.12-20261031160759-53d5feeb32e1.2
	github.com/anyascii/go v0.3.3
	github.com/bits-and-blooms/bitset v1.29.0
	github.com/blugelabs/bluge v0.2.2
	github.com/blugelabs/bluge_segment_api v0.2.0
	github.com/blugelabs/query_string v0.3.0
	github.com/bwmarrin/discordgo v0.29.0
	github.com/dgryski/dgoogauth v0.0.0-20190221195224-5a805980a5f3
	github.com/dop251/goja v0.0.0-20261103214539-f5d1d0025744
	github.com/echotools/nevr-fleetmanager v0.0.0-20260524154545-54aa6f7dd410
	github.com/echotools/vrmlgo/v5 v5.0.1
	github.com/go-redis/redis v6.15.9+incompatible
	github.com/go-restruct/restruct v1.2.0-alpha
	github.com/gofrs/uuid/v5 v5.5.1
	github.com/golang-jwt/jwt/v5 v5.3.1
	github.com/google/go-cmp v0.7.0
	github.com/gorilla/handlers v1.5.2
	github.com/gorilla/mux v1.8.1
	github.com/gorilla/websocket v1.5.3
	github.com/grpc-ecosystem/grpc-gateway/v2 v2.33.0
	github.com/heroiclabs/nakama-common v1.44.1
	github.com/heroiclabs/sql-migrate v0.0.0-20260723131912-797198e52567
	github.com/intinig/go-openskill v0.0.0-20260914095109-656cbb57d714
	github.com/jackc/pgerrcode v0.0.0-20250907135507-afb5586c32a6
	github.com/jackc/pgtype v1.14.4
	github.com/jackc/pgx/v5 v5.12.0
	github.com/klauspost/compress v1.20.3
	github.com/mmcloughlin/geohash v0.10.0
	github.com/muesli/reflow v0.3.0
	github.com/prometheus/client_golang v1.25.0
	github.com/prometheus/common v0.73.1
	github.com/samber/lo v1.53.0
	github.com/stretchr/testify v1.12.1
	github.com/uber-go/tally/v4 v4.1.17
	go.mongodb.org/mongo-driver v1.17.10
	go.uber.org/atomic v1.12.0
	go.uber.org/thriftrw v1.34.0
	go.uber.org/zap v1.28.0
	golang.org/x/crypto v0.58.0
	golang.org/x/oauth2 v0.37.0
	golang.org/x/sync v0.24.0
	golang.org/x/time v0.16.0
	gonum.org/v1/gonum v0.17.0
	google.golang.org/genproto/googleapis/api v0.0.0-20261026165753-2ffab32bce7e
	google.golang.org/grpc v1.85.0
	google.golang.org/grpc/cmd/protoc-gen-go-grpc v1.6.2
	google.golang.org/protobuf v1.36.12
	gopkg.in/natefinch/lumberjack.v2 v2.2.1
	gopkg.in/yaml.v3 v3.0.1
)

require (
	cloud.google.com/go/compute/metadata v0.10.0 // indirect
	github.com/RoaringBitmap/roaring v1.9.4 // indirect
	github.com/axiomhq/hyperloglog v0.3.0 // indirect
	github.com/beorn7/perks v1.0.1 // indirect
	github.com/blevesearch/go-porterstemmer v1.0.3 // indirect
	github.com/blevesearch/mmap-go v1.2.0 // indirect
	github.com/blevesearch/segment v0.9.1 // indirect
	github.com/blevesearch/snowballstem v0.9.0 // indirect
	github.com/blevesearch/vellum v1.2.1 // indirect
	github.com/blugelabs/ice v1.0.0 // indirect
	github.com/blugelabs/ice/v2 v2.0.1 // indirect
	github.com/caio/go-tdigest v3.1.0+incompatible // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/clipperhouse/uax29/v2 v2.7.0 // indirect
	github.com/davecgh/go-spew v1.1.2-0.20180830191138-d8f796af33cc // indirect
	github.com/dgryski/go-metro v0.0.0-20261008173524-5059e0b1da03 // indirect
	github.com/dlclark/regexp2/v2 v2.12.0 // indirect
	github.com/felixge/httpsnoop v1.1.0 // indirect
	github.com/fsnotify/fsnotify v1.9.0 // indirect
	github.com/go-sourcemap/sourcemap v2.1.4+incompatible // indirect
	github.com/golang/mock v1.6.0 // indirect
	github.com/golang/snappy v1.0.0 // indirect
	github.com/google/pprof v0.0.0-20261104075808-393459d12e44 // indirect
	github.com/jackc/pgio v1.0.0 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.3 // indirect
	github.com/kamstrup/intmap v0.5.2 // indirect
	github.com/mattn/go-runewidth v0.0.31 // indirect
	github.com/montanaflynn/stats v0.14.0 // indirect
	github.com/mschoch/smat v0.2.0 // indirect
	github.com/munnerz/goautoneg v0.0.0-20191010083416-a7dc8b61c822 // indirect
	github.com/nxadm/tail v1.4.11 // indirect
	github.com/onsi/ginkgo v1.16.5 // indirect
	github.com/onsi/gomega v1.37.0 // indirect
	github.com/pandatix/go-abnf v0.5.5 // indirect
	github.com/pkg/errors v0.9.1 // indirect
	github.com/prometheus/client_model v0.6.3 // indirect
	github.com/prometheus/procfs v0.23.0 // indirect
	github.com/rogpeppe/go-internal v1.14.1 // indirect
	github.com/twmb/murmur3 v1.2.2 // indirect
	github.com/xdg-go/pbkdf2 v1.0.0 // indirect
	github.com/xdg-go/scram v1.3.0 // indirect
	github.com/xdg-go/stringprep v1.0.4 // indirect
	github.com/youmark/pkcs8 v0.0.0-20240726163527-a2c0da244d78 // indirect
	go.uber.org/multierr v1.11.0 // indirect
	go.yaml.in/yaml/v2 v2.4.5 // indirect
	go.yaml.in/yaml/v3 v3.0.5 // indirect
	golang.org/x/net v0.62.0 // indirect
	golang.org/x/sys v0.49.0 // indirect
	golang.org/x/text v0.43.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20261026165753-2ffab32bce7e // indirect
)
