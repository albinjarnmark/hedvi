# Third-party notices

Hedvi uses third-party software under its original licenses. The AGPL license
for Hedvi does not replace those licenses or transfer their copyrights.

The machine-readable [dependency inventory](third_party/dependencies.json) records
the installed Go and npm dependencies and links to retained license texts under
`third_party/licenses/`. Versions come from `go.mod`/`go.sum` and `pnpm-lock.yaml`.
This is a source release: it does not redistribute node_modules, prebuilt native
libraries, Docker images or the Go toolchain.

The inventory is from a macOS ARM64 installation. Optional packages for other
platforms and notices bundled inside packages such as Next.js must be inventoried
from the actual target build before distributing a binary or container image.
In particular libvips is LGPL-licensed; preserve its notices and satisfy its source
and relinking obligations when redistributing builds that include it.

The hosted billing integration uses the MIT-licensed Stripe Go SDK and shopspring/decimal. Their complete notices are retained in the dependency inventory.

The Python contract test uses the MIT-licensed Resend SDK, installed separately
from PyPI under `contracts/python/requirements.txt`. AWS SDK for Go is Apache-2.0;
pgx is MIT; Go x/* libraries use BSD-style licenses. Their full notices take
precedence over this summary. The Standard Webhooks test vector is attributed in
its test file; see https://github.com/standard-webhooks/standard-webhooks.

Infrastructure templates invoke independently licensed services and tools.
Publishing Terraform configuration does not sublicense Terraform or AWS/Railway.
No affiliation with or endorsement by Resend is implied.

Inventory: 457 dependency records.

| Ecosystem | Package | Version(s) | Declared license / original notice |
|---|---|---|---|
| go | `github.com/alecthomas/kingpin/v2` | v2.4.0 | See retained license text |
| go | `github.com/alecthomas/units` | v0.0.0-20240927000941-0f3dac36c52b | See retained license text |
| go | `github.com/aws/aws-sdk-go-v2` | v1.48.0 | See retained license text |
| go | `github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream` | v1.7.27 | See retained license text |
| go | `github.com/aws/aws-sdk-go-v2/config` | v1.33.21 | See retained license text |
| go | `github.com/aws/aws-sdk-go-v2/credentials` | v1.21.3 | See retained license text |
| go | `github.com/aws/aws-sdk-go-v2/feature/ec2/imds` | v1.20.9 | See retained license text |
| go | `github.com/aws/aws-sdk-go-v2/internal/configsources` | v1.5.12 | See retained license text |
| go | `github.com/aws/aws-sdk-go-v2/internal/endpoints/v2` | v2.8.12 | See retained license text |
| go | `github.com/aws/aws-sdk-go-v2/internal/v4a` | v1.5.12 | See retained license text |
| go | `github.com/aws/aws-sdk-go-v2/service/internal/accept-encoding` | v1.13.25 | See retained license text |
| go | `github.com/aws/aws-sdk-go-v2/service/internal/checksum` | v1.11.13 | See retained license text |
| go | `github.com/aws/aws-sdk-go-v2/service/internal/presigned-url` | v1.14.12 | See retained license text |
| go | `github.com/aws/aws-sdk-go-v2/service/internal/s3shared` | v1.20.12 | See retained license text |
| go | `github.com/aws/aws-sdk-go-v2/service/s3` | v1.116.0 | See retained license text |
| go | `github.com/aws/aws-sdk-go-v2/service/sesv2` | v1.80.0 | See retained license text |
| go | `github.com/aws/aws-sdk-go-v2/service/signin` | v1.14.0 | See retained license text |
| go | `github.com/aws/aws-sdk-go-v2/service/sns` | v1.48.0 | See retained license text |
| go | `github.com/aws/aws-sdk-go-v2/service/sqs` | v1.53.0 | See retained license text |
| go | `github.com/aws/aws-sdk-go-v2/service/sso` | v1.39.5 | See retained license text |
| go | `github.com/aws/aws-sdk-go-v2/service/ssooidc` | v1.44.5 | See retained license text |
| go | `github.com/aws/aws-sdk-go-v2/service/sts` | v1.53.0 | See retained license text |
| go | `github.com/aws/smithy-go` | v1.28.9 | See retained license text |
| go | `github.com/beorn7/perks` | v1.0.1 | See retained license text |
| go | `github.com/cespare/xxhash/v2` | v2.3.0 | See retained license text |
| go | `github.com/davecgh/go-spew` | v1.1.1 | See retained license text |
| go | `github.com/golang-jwt/jwt/v5` | v5.3.1 | See retained license text |
| go | `github.com/golang/protobuf` | v1.5.0 | See retained license text |
| go | `github.com/google/go-cmp` | v0.7.0 | See retained license text |
| go | `github.com/google/uuid` | v1.6.0 | See retained license text |
| go | `github.com/jackc/pgpassfile` | v1.0.0 | See retained license text |
| go | `github.com/jackc/pgservicefile` | v0.0.0-20240606120523-5a60cdf6a761 | See retained license text |
| go | `github.com/jackc/pgx/v5` | v5.13.0 | See retained license text |
| go | `github.com/jackc/puddle/v2` | v2.2.3 | See retained license text |
| go | `github.com/jpillora/backoff` | v1.0.0 | See retained license text |
| go | `github.com/json-iterator/go` | v1.1.12 | See retained license text |
| go | `github.com/julienschmidt/httprouter` | v1.3.0 | See retained license text |
| go | `github.com/klauspost/compress` | v1.20.1 | See retained license text |
| go | `github.com/kr/pretty` | v0.3.0 | See retained license text |
| go | `github.com/kylelemons/godebug` | v1.1.0 | See retained license text |
| go | `github.com/modern-go/concurrent` | v0.0.0-20180306012644-bacd9c7ef1dd | See retained license text |
| go | `github.com/modern-go/reflect2` | v1.0.2 | See retained license text |
| go | `github.com/munnerz/goautoneg` | v0.0.0-20191010083416-a7dc8b61c822 | See retained license text |
| go | `github.com/mwitkow/go-conntrack` | v0.0.0-20190716064945-2f068394615f | See retained license text |
| go | `github.com/onsi/gomega` | v1.44.0 | See retained license text |
| go | `github.com/pmezard/go-difflib` | v1.0.0 | See retained license text |
| go | `github.com/prometheus/client_golang` | v1.25.0 | See retained license text |
| go | `github.com/prometheus/client_model` | v0.6.3 | See retained license text |
| go | `github.com/prometheus/common` | v0.72.0 | See retained license text |
| go | `github.com/prometheus/procfs` | v0.22.0 | See retained license text |
| go | `github.com/stretchr/objx` | v0.1.0 | See retained license text |
| go | `github.com/stretchr/testify` | v1.12.1 | See retained license text |
| go | `github.com/valkey-io/valkey-go` | v1.0.80 | See retained license text |
| go | `github.com/xhit/go-str2duration/v2` | v2.1.0 | See retained license text |
| go | `go.uber.org/goleak` | v1.3.0 | See retained license text |
| go | `go.yaml.in/yaml/v2` | v2.4.4 | See retained license text |
| go | `go.yaml.in/yaml/v3` | v3.0.6 | See retained license text |
| go | `golang.org/x/crypto` | v0.59.0 | See retained license text |
| go | `golang.org/x/mod` | v0.42.0 | See retained license text |
| go | `golang.org/x/net` | v0.63.0 | See retained license text |
| go | `golang.org/x/oauth2` | v0.37.0 | See retained license text |
| go | `golang.org/x/sync` | v0.25.0 | See retained license text |
| go | `golang.org/x/sys` | v0.50.0 | See retained license text |
| go | `golang.org/x/term` | v0.48.0 | See retained license text |
| go | `golang.org/x/text` | v0.44.0 | See retained license text |
| go | `golang.org/x/tools` | v0.52.0 | See retained license text |
| go | `google.golang.org/protobuf` | v1.36.12 | See retained license text |
| go | `gopkg.in/check.v1` | v1.0.0-20201130134442-10cb98267c6c | See retained license text |
| go | `gopkg.in/yaml.v3` | v3.0.1 | See retained license text |
| npm | `@babel/code-frame` | 7.29.7 | MIT |
| npm | `@babel/compat-data` | 7.29.7 | MIT |
| npm | `@babel/core` | 7.29.13 | MIT |
| npm | `@babel/generator` | 7.29.15 | MIT |
| npm | `@babel/helper-compilation-targets` | 7.29.13 | MIT |
| npm | `@babel/helper-globals` | 7.29.7 | MIT |
| npm | `@babel/helper-module-imports` | 7.29.7 | MIT |
| npm | `@babel/helper-module-transforms` | 7.29.14 | MIT |
| npm | `@babel/helper-string-parser` | 7.29.15 | MIT |
| npm | `@babel/helper-validator-identifier` | 7.29.7 | MIT |
| npm | `@babel/helper-validator-option` | 7.29.7 | MIT |
| npm | `@babel/helpers` | 7.29.15 | MIT |
| npm | `@babel/parser` | 7.29.15 | MIT |
| npm | `@babel/template` | 7.29.7 | MIT |
| npm | `@babel/traverse` | 7.29.15 | MIT |
| npm | `@babel/types` | 7.29.15 | MIT |
| npm | `@cacheable/memory` | 2.2.0 | MIT |
| npm | `@cacheable/utils` | 2.5.0 | MIT |
| npm | `@eslint-community/eslint-utils` | 4.9.1, 4.10.1 | MIT |
| npm | `@eslint-community/regexpp` | 4.12.3 | MIT |
| npm | `@eslint/config-array` | 0.23.6 | Apache-2.0 |
| npm | `@eslint/config-helpers` | 0.7.0 | Apache-2.0 |
| npm | `@eslint/core` | 1.2.1 | Apache-2.0 |
| npm | `@eslint/object-schema` | 3.0.5 | Apache-2.0 |
| npm | `@eslint/plugin-kit` | 0.7.3 | Apache-2.0 |
| npm | `@humanfs/core` | 0.19.2 | Apache-2.0 |
| npm | `@humanfs/node` | 0.16.8 | Apache-2.0 |
| npm | `@humanfs/types` | 0.15.0 | Apache-2.0 |
| npm | `@humanwhocodes/module-importer` | 1.0.1 | Apache-2.0 |
| npm | `@humanwhocodes/retry` | 0.4.3 | Apache-2.0 |
| npm | `@img/colour` | 1.1.0 | MIT |
| npm | `@img/sharp-darwin-arm64` | 0.35.5 | Apache-2.0 |
| npm | `@img/sharp-libvips-darwin-arm64` | 1.3.4 | LGPL-3.0-or-later |
| npm | `@isaacs/nonrecursive-glob-match` | 1.1.0 | BlueOak-1.0.0 |
| npm | `@jridgewell/gen-mapping` | 0.3.13 | MIT |
| npm | `@jridgewell/remapping` | 2.3.5 | MIT |
| npm | `@jridgewell/resolve-uri` | 3.1.3 | MIT |
| npm | `@jridgewell/sourcemap-codec` | 1.6.0 | MIT |
| npm | `@jridgewell/trace-mapping` | 0.3.31 | MIT |
| npm | `@keyv/bigmap` | 1.3.1 | MIT |
| npm | `@keyv/serialize` | 1.1.1 | MIT |
| npm | `@next/env` | 16.4.6 | MIT |
| npm | `@next/eslint-plugin-next` | 16.4.6 | MIT |
| npm | `@next/swc-darwin-arm64` | 16.4.6 | MIT |
| npm | `@nodelib/fs.scandir` | 2.1.5 | MIT |
| npm | `@nodelib/fs.stat` | 2.0.5 | MIT |
| npm | `@nodelib/fs.walk` | 1.2.8 | MIT |
| npm | `@nolyfill/is-core-module` | 1.0.39 | MIT |
| npm | `@oxc-project/types` | 0.160.0 | MIT |
| npm | `@rolldown/binding-darwin-arm64` | 1.3.4 | MIT |
| npm | `@rolldown/pluginutils` | 1.1.1 | MIT |
| npm | `@rtsao/scc` | 1.1.0 | MIT |
| npm | `@stablelib/base64` | 1.0.1 | MIT |
| npm | `@swc/helpers` | 0.5.23 | Apache-2.0 |
| npm | `@types/chai` | 5.2.3 | MIT |
| npm | `@types/deep-eql` | 4.0.2 | MIT |
| npm | `@types/esrecurse` | 4.3.1 | MIT |
| npm | `@types/estree` | 1.0.11 | MIT |
| npm | `@types/json-schema` | 7.0.15 | MIT |
| npm | `@types/json5` | 0.0.29 | MIT |
| npm | `@types/node` | 24.19.2 | MIT |
| npm | `@types/react` | 19.3.0 | MIT |
| npm | `@types/react-dom` | 19.3.0 | MIT |
| npm | `@typescript-eslint/eslint-plugin` | 8.74.0 | MIT |
| npm | `@typescript-eslint/parser` | 8.74.0 | MIT |
| npm | `@typescript-eslint/project-service` | 8.74.0 | MIT |
| npm | `@typescript-eslint/scope-manager` | 8.74.0 | MIT |
| npm | `@typescript-eslint/tsconfig-utils` | 8.74.0 | MIT |
| npm | `@typescript-eslint/type-utils` | 8.74.0 | MIT |
| npm | `@typescript-eslint/types` | 8.74.0 | MIT |
| npm | `@typescript-eslint/typescript-estree` | 8.74.0 | MIT |
| npm | `@typescript-eslint/utils` | 8.74.0 | MIT |
| npm | `@typescript-eslint/visitor-keys` | 8.74.0 | MIT |
| npm | `@unrs/resolver-binding-darwin-arm64` | 1.13.0 | MIT |
| npm | `@vitest/mocker` | 5.1.0 | MIT |
| npm | `@vitest/spy` | 5.1.0 | MIT |
| npm | `acorn` | 8.19.0 | MIT |
| npm | `acorn-jsx` | 5.3.2 | MIT |
| npm | `ajv` | 6.15.0 | MIT |
| npm | `aria-query` | 5.3.2 | Apache-2.0 |
| npm | `array-buffer-byte-length` | 1.0.2 | MIT |
| npm | `array-includes` | 3.2.0 | MIT |
| npm | `array.prototype.findlast` | 1.2.5 | MIT |
| npm | `array.prototype.findlastindex` | 1.2.6 | MIT |
| npm | `array.prototype.flat` | 1.3.3 | MIT |
| npm | `array.prototype.flatmap` | 1.3.3 | MIT |
| npm | `array.prototype.tosorted` | 1.1.4 | MIT |
| npm | `arraybuffer.prototype.slice` | 1.0.4 | MIT |
| npm | `assertion-error` | 2.0.1 | MIT |
| npm | `ast-types-flow` | 0.0.8 | MIT |
| npm | `async-function` | 1.0.0 | MIT |
| npm | `available-typed-arrays` | 1.0.7 | MIT |
| npm | `axe-core` | 4.14.1 | MPL-2.0 |
| npm | `axobject-query` | 4.1.0 | Apache-2.0 |
| npm | `balanced-match` | 1.0.2, 4.0.4 | MIT |
| npm | `baseline-browser-mapping` | 2.11.40 | Apache-2.0 |
| npm | `brace-expansion` | 1.1.22, 5.1.0 | MIT |
| npm | `braces` | 3.0.3 | MIT |
| npm | `browserslist` | 4.29.6 | MIT |
| npm | `cacheable` | 2.5.0 | MIT |
| npm | `call-bind` | 1.0.9 | MIT |
| npm | `call-bind-apply-helpers` | 1.0.2 | MIT |
| npm | `call-bound` | 1.0.4 | MIT |
| npm | `caniuse-lite` | 1.0.30001828 | CC-BY-4.0 |
| npm | `chai` | 6.3.0 | MIT |
| npm | `client-only` | 0.0.1 | MIT |
| npm | `concat-map` | 0.0.1 | MIT |
| npm | `convert-source-map` | 2.0.0 | MIT |
| npm | `cross-spawn` | 7.0.6 | MIT |
| npm | `csstype` | 3.2.4 | MIT |
| npm | `damerau-levenshtein` | 1.0.8 | BSD-2-Clause |
| npm | `data-view-buffer` | 1.0.2 | MIT |
| npm | `data-view-byte-length` | 1.0.2 | MIT |
| npm | `data-view-byte-offset` | 1.0.1 | MIT |
| npm | `debug` | 3.2.7, 4.4.3 | MIT |
| npm | `deep-is` | 0.1.4 | MIT |
| npm | `define-data-property` | 1.1.4 | MIT |
| npm | `define-properties` | 1.2.1 | MIT |
| npm | `detect-libc` | 2.1.2 | Apache-2.0 |
| npm | `doctrine` | 2.1.0 | Apache-2.0 |
| npm | `dunder-proto` | 1.0.1 | MIT |
| npm | `electron-to-chromium` | 1.5.495 | ISC |
| npm | `emoji-regex` | 9.2.2 | MIT |
| npm | `entities` | 6.0.1 | BSD-2-Clause |
| npm | `es-abstract` | 1.24.2 | MIT |
| npm | `es-abstract-get` | 1.0.0 | MIT |
| npm | `es-define-property` | 1.0.1 | MIT |
| npm | `es-errors` | 1.3.0 | MIT |
| npm | `es-iterator-helpers` | 1.4.0 | MIT |
| npm | `es-module-lexer` | 2.3.2 | MIT |
| npm | `es-object-atoms` | 1.1.2 | MIT |
| npm | `es-set-tostringtag` | 2.1.0 | MIT |
| npm | `es-shim-unscopables` | 1.1.0 | MIT |
| npm | `es-to-primitive` | 1.3.4 | MIT |
| npm | `escalade` | 3.2.0 | MIT |
| npm | `escape-string-regexp` | 4.0.0 | MIT |
| npm | `eslint` | 10.14.0 | MIT |
| npm | `eslint-config-next` | 16.4.6 | MIT |
| npm | `eslint-import-resolver-node` | 0.3.10 | MIT |
| npm | `eslint-import-resolver-typescript` | 3.10.1 | ISC |
| npm | `eslint-module-utils` | 2.14.0 | MIT |
| npm | `eslint-plugin-import` | 2.32.0 | MIT |
| npm | `eslint-plugin-jsx-a11y` | 6.10.2 | MIT |
| npm | `eslint-plugin-react` | 7.37.5 | MIT |
| npm | `eslint-plugin-react-hooks` | 7.1.1 | MIT |
| npm | `eslint-scope` | 9.1.2 | BSD-2-Clause |
| npm | `eslint-visitor-keys` | 3.4.3, 5.0.1 | Apache-2.0 |
| npm | `espree` | 11.2.0 | BSD-2-Clause |
| npm | `esquery` | 1.7.0 | BSD-3-Clause |
| npm | `esrecurse` | 4.3.0 | BSD-2-Clause |
| npm | `estraverse` | 5.3.0 | BSD-2-Clause |
| npm | `estree-walker` | 3.0.3 | MIT |
| npm | `esutils` | 2.0.3 | BSD-2-Clause |
| npm | `expect-type` | 1.4.0 | Apache-2.0 |
| npm | `fast-deep-equal` | 3.1.3 | MIT |
| npm | `fast-glob` | 3.3.1 | MIT |
| npm | `fast-json-stable-stringify` | 2.1.0 | MIT |
| npm | `fast-levenshtein` | 2.0.6 | MIT |
| npm | `fast-sha256` | 1.3.0 | Unlicense |
| npm | `fastq` | 1.20.3 | ISC |
| npm | `fdir` | 6.5.0 | MIT |
| npm | `file-entry-cache` | 11.1.5 | MIT |
| npm | `fill-range` | 7.1.1 | MIT |
| npm | `find-up` | 5.0.0 | MIT |
| npm | `flat-cache` | 6.1.23 | MIT |
| npm | `flatted` | 3.4.4 | ISC |
| npm | `for-each` | 0.3.5 | MIT |
| npm | `fsevents` | 2.3.3 | MIT |
| npm | `function-bind` | 1.1.2 | MIT |
| npm | `function.prototype.name` | 1.2.0 | MIT |
| npm | `functions-have-names` | 1.2.3 | MIT |
| npm | `generator-function` | 2.0.1 | MIT |
| npm | `gensync` | 1.0.0-beta.2 | MIT |
| npm | `get-intrinsic` | 1.3.0 | MIT |
| npm | `get-proto` | 1.0.1 | MIT |
| npm | `get-symbol-description` | 1.1.0 | MIT |
| npm | `get-tsconfig` | 4.14.3 | MIT |
| npm | `glob-parent` | 5.1.2, 6.0.2 | ISC |
| npm | `globals` | 16.4.0 | MIT |
| npm | `globalthis` | 1.0.4 | MIT |
| npm | `gopd` | 1.2.0 | MIT |
| npm | `has-bigints` | 1.1.0 | MIT |
| npm | `has-property-descriptors` | 1.0.2 | MIT |
| npm | `has-proto` | 1.2.0 | MIT |
| npm | `has-symbols` | 1.1.0 | MIT |
| npm | `has-tostringtag` | 1.0.2 | MIT |
| npm | `hashery` | 1.5.1 | MIT |
| npm | `hasown` | 2.0.4 | MIT |
| npm | `hermes-estree` | 0.25.1 | MIT |
| npm | `hermes-parser` | 0.25.1 | MIT |
| npm | `hookified` | 1.15.1, 2.2.0 | MIT |
| npm | `ignore` | 5.3.2, 7.0.17 | MIT |
| npm | `imurmurhash` | 0.1.4 | MIT |
| npm | `internal-slot` | 1.1.0 | MIT |
| npm | `is-array-buffer` | 3.0.5 | MIT |
| npm | `is-async-function` | 2.1.1 | MIT |
| npm | `is-bigint` | 1.1.0 | MIT |
| npm | `is-boolean-object` | 1.2.2 | MIT |
| npm | `is-bun-module` | 2.0.0 | MIT |
| npm | `is-callable` | 1.2.7 | MIT |
| npm | `is-core-module` | 2.18.0 | MIT |
| npm | `is-data-view` | 1.0.2 | MIT |
| npm | `is-date-object` | 1.1.0 | MIT |
| npm | `is-document.all` | 1.0.0 | MIT |
| npm | `is-extglob` | 2.1.1 | MIT |
| npm | `is-finalizationregistry` | 1.1.1 | MIT |
| npm | `is-generator-function` | 1.1.2 | MIT |
| npm | `is-glob` | 4.0.3 | MIT |
| npm | `is-map` | 2.0.3 | MIT |
| npm | `is-negative-zero` | 2.0.3 | MIT |
| npm | `is-number` | 7.0.0 | MIT |
| npm | `is-number-object` | 1.1.1 | MIT |
| npm | `is-regex` | 1.2.1 | MIT |
| npm | `is-set` | 2.0.3 | MIT |
| npm | `is-shared-array-buffer` | 1.0.4 | MIT |
| npm | `is-string` | 1.1.1 | MIT |
| npm | `is-symbol` | 1.1.1 | MIT |
| npm | `is-typed-array` | 1.1.15 | MIT |
| npm | `is-weakmap` | 2.0.2 | MIT |
| npm | `is-weakref` | 1.1.1 | MIT |
| npm | `is-weakset` | 2.0.4 | MIT |
| npm | `isarray` | 2.0.5 | MIT |
| npm | `isexe` | 2.0.0 | ISC |
| npm | `iterator.prototype` | 1.1.5 | MIT |
| npm | `js-tokens` | 4.0.0 | MIT |
| npm | `jsesc` | 3.1.0 | MIT |
| npm | `json-schema-traverse` | 0.4.1 | MIT |
| npm | `json-stable-stringify-without-jsonify` | 1.0.1 | MIT |
| npm | `json5` | 1.0.2, 2.2.3 | MIT |
| npm | `jsx-ast-utils` | 3.3.5 | MIT |
| npm | `keyv` | 5.6.0 | MIT |
| npm | `language-subtag-registry` | 0.3.23 | CC0-1.0 |
| npm | `language-tags` | 1.0.9 | MIT |
| npm | `levn` | 0.4.1 | MIT |
| npm | `lightningcss` | 1.33.0 | MPL-2.0 |
| npm | `lightningcss-darwin-arm64` | 1.33.0 | MPL-2.0 |
| npm | `locate-path` | 6.0.0 | MIT |
| npm | `loose-envify` | 1.4.0 | MIT |
| npm | `lru-cache` | 5.1.1 | ISC |
| npm | `magic-string` | 1.4.5 | MIT |
| npm | `math-intrinsics` | 1.1.0 | MIT |
| npm | `merge2` | 1.4.1 | MIT |
| npm | `micromatch` | 4.0.8 | MIT |
| npm | `minimatch` | 11.0.1, 11.0.9 | BlueOak-1.0.0 |
| npm | `minimatch` | 3.1.5 | ISC |
| npm | `minimist` | 1.2.8 | MIT |
| npm | `ms` | 2.1.3 | MIT |
| npm | `nanoid` | 3.3.20 | MIT |
| npm | `napi-postinstall` | 0.3.8 | MIT |
| npm | `natural-compare` | 1.4.0 | MIT |
| npm | `next` | 16.4.6 | MIT |
| npm | `node-exports-info` | 1.6.2 | MIT |
| npm | `node-releases` | 2.0.66 | MIT |
| npm | `object-assign` | 4.1.1 | MIT |
| npm | `object-inspect` | 1.14.0 | MIT |
| npm | `object-keys` | 1.1.1 | MIT |
| npm | `object.assign` | 4.1.7 | MIT |
| npm | `object.entries` | 1.1.9 | MIT |
| npm | `object.fromentries` | 2.0.8 | MIT |
| npm | `object.groupby` | 1.0.3 | MIT |
| npm | `object.values` | 1.2.1 | MIT |
| npm | `obug` | 3.0.0 | MIT |
| npm | `optionator` | 0.9.4 | MIT |
| npm | `own-keys` | 1.0.2 | MIT |
| npm | `p-limit` | 3.1.0 | MIT |
| npm | `p-locate` | 5.0.0 | MIT |
| npm | `parse5` | 7.3.0 | MIT |
| npm | `path-exists` | 4.0.0 | MIT |
| npm | `path-key` | 3.1.1 | MIT |
| npm | `path-parse` | 1.0.7 | MIT |
| npm | `picocolors` | 1.1.1 | ISC |
| npm | `picomatch` | 2.3.2, 4.0.7 | MIT |
| npm | `possible-typed-array-names` | 1.1.0 | MIT |
| npm | `postal-mime` | 2.7.6 | MIT-0 |
| npm | `postcss` | 8.5.23, 8.5.33 | MIT |
| npm | `prelude-ls` | 1.2.1 | MIT |
| npm | `prop-types` | 15.8.1 | MIT |
| npm | `punycode` | 2.3.1 | MIT |
| npm | `qified` | 0.10.1 | MIT |
| npm | `queue-microtask` | 1.2.3 | MIT |
| npm | `react` | 19.3.1 | MIT |
| npm | `react-dom` | 19.3.1 | MIT |
| npm | `react-is` | 16.13.1 | MIT |
| npm | `reflect.getprototypeof` | 1.0.10 | MIT |
| npm | `regexp.prototype.flags` | 1.5.4 | MIT |
| npm | `resend` | 6.39.0 | MIT |
| npm | `resolve` | 2.0.0-next.7 | MIT |
| npm | `resolve-pkg-maps` | 1.0.0 | MIT |
| npm | `reusify` | 1.1.0 | MIT |
| npm | `rolldown` | 1.3.4 | MIT |
| npm | `run-parallel` | 1.2.0 | MIT |
| npm | `safe-array-concat` | 1.1.4 | MIT |
| npm | `safe-push-apply` | 1.0.0 | MIT |
| npm | `safe-regex-test` | 1.1.0 | MIT |
| npm | `scheduler` | 0.28.0 | MIT |
| npm | `semver` | 6.3.2, 7.8.6 | ISC |
| npm | `set-function-length` | 1.2.2 | MIT |
| npm | `set-function-name` | 2.0.2 | MIT |
| npm | `set-proto` | 1.0.0 | MIT |
| npm | `sharp` | 0.35.5 | Apache-2.0 |
| npm | `shebang-command` | 2.0.0 | MIT |
| npm | `shebang-regex` | 3.0.0 | MIT |
| npm | `side-channel` | 1.1.1 | MIT |
| npm | `side-channel-list` | 1.0.1 | MIT |
| npm | `side-channel-map` | 1.0.1 | MIT |
| npm | `side-channel-weakmap` | 1.0.2 | MIT |
| npm | `source-map-js` | 1.2.4 | BSD-3-Clause |
| npm | `stable-hash` | 0.0.5 | MIT |
| npm | `standardwebhooks` | 1.1.1 | MIT |
| npm | `std-env` | 4.3.0 | MIT |
| npm | `stop-iteration-iterator` | 1.1.0 | MIT |
| npm | `string.prototype.includes` | 2.0.1 | MIT |
| npm | `string.prototype.matchall` | 4.1.0 | MIT |
| npm | `string.prototype.repeat` | 1.0.0 | MIT |
| npm | `string.prototype.trim` | 1.2.11 | MIT |
| npm | `string.prototype.trimend` | 1.0.10 | MIT |
| npm | `string.prototype.trimstart` | 1.0.8 | MIT |
| npm | `strip-bom` | 3.0.0 | MIT |
| npm | `styled-jsx` | 5.1.6 | MIT |
| npm | `supports-preserve-symlinks-flag` | 1.0.0 | MIT |
| npm | `tinybench` | 6.2.5 | MIT |
| npm | `tinyexec` | 1.4.0 | MIT |
| npm | `tinyglobby` | 0.2.17 | MIT |
| npm | `to-regex-range` | 5.0.1 | MIT |
| npm | `ts-api-utils` | 2.5.0 | MIT |
| npm | `tsconfig-paths` | 3.15.0 | MIT |
| npm | `tslib` | 2.9.1 | 0BSD |
| npm | `type-check` | 0.4.0 | MIT |
| npm | `typed-array-buffer` | 1.0.3 | MIT |
| npm | `typed-array-byte-length` | 1.0.3 | MIT |
| npm | `typed-array-byte-offset` | 1.0.5 | MIT |
| npm | `typed-array-length` | 1.0.8 | MIT |
| npm | `typescript` | 6.0.3 | Apache-2.0 |
| npm | `typescript-eslint` | 8.74.0 | MIT |
| npm | `unbox-primitive` | 1.1.0 | MIT |
| npm | `undici-types` | 7.24.6 | MIT |
| npm | `unrs-resolver` | 1.13.0 | MIT |
| npm | `update-browserslist-db` | 1.4.0 | MIT |
| npm | `uri-js` | 4.4.1 | BSD-2-Clause |
| npm | `vite` | 8.4.0 | MIT |
| npm | `vitest` | 5.1.0 | MIT |
| npm | `which` | 2.0.2 | ISC |
| npm | `which-boxed-primitive` | 1.1.1 | MIT |
| npm | `which-builtin-type` | 1.2.1 | MIT |
| npm | `which-collection` | 1.0.2 | MIT |
| npm | `which-typed-array` | 1.1.24 | MIT |
| npm | `why-is-node-running` | 3.2.1 | MIT |
| npm | `word-wrap` | 1.2.5 | MIT |
| npm | `yallist` | 3.1.1 | ISC |
| npm | `yocto-queue` | 0.1.0 | MIT |
| npm | `zod` | 4.6.7 | MIT |
| npm | `zod-validation-error` | 4.0.2 | MIT |
| npm | `pngjs` | 5.0.0 | MIT |
| npm | `get-caller-file` | 2.0.5 | ISC |
| npm | `playwright-core` | 1.56.1 | Apache-2.0 |
| npm | `find-up` | 4.1.0 | MIT |
| npm | `locate-path` | 5.0.0 | MIT |
| npm | `p-locate` | 4.1.0 | MIT |
| npm | `p-limit` | 2.3.0 | MIT |
| npm | `set-blocking` | 2.0.0 | ISC |
| npm | `require-directory` | 2.1.1 | MIT |
| npm | `p-try` | 2.2.0 | MIT |
| npm | `strip-ansi` | 6.0.1 | MIT |
| npm | `wrap-ansi` | 6.2.0 | MIT |
| npm | `ansi-styles` | 4.3.0 | MIT |
| npm | `string-width` | 4.2.3 | MIT |
| npm | `fsevents` | 2.3.2 | MIT |
| npm | `color-name` | 1.1.4 | MIT |
| npm | `color-convert` | 2.0.1 | MIT |
| npm | `yargs-parser` | 18.1.3 | ISC |
| npm | `decamelize` | 1.2.0 | MIT |
| npm | `camelcase` | 5.3.1 | MIT |
| npm | `emoji-regex` | 8.0.0 | MIT |
| npm | `require-main-filename` | 2.0.0 | ISC |
| npm | `y18n` | 4.0.3 | ISC |
| npm | `which-module` | 2.0.1 | ISC |
| npm | `cliui` | 6.0.0 | ISC |
| npm | `yargs` | 15.4.1 | MIT |
| npm | `ansi-regex` | 5.0.1 | MIT |
| npm | `dijkstrajs` | 1.0.3 | MIT |
| npm | `qrcode` | 1.5.4 | MIT |
| npm | `is-fullwidth-code-point` | 3.0.0 | MIT |
| npm | `minimatch` | 10.2.6 | BlueOak-1.0.0 |
| npm | `playwright` | 1.56.1 | Apache-2.0 |
| npm | `@types/qrcode` | 1.5.5 | MIT |
| npm | `@playwright/test` | 1.56.1 | Apache-2.0 |
