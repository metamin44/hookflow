# HookFlow — گزارش نهایی اجرای Bundle 1

تاریخ: 2026-11-24. مبنا: دستور Owner-approved Full Implementation؛ بدون تغییر تصمیم‌های خارج از D1a/D1b/D2b/D2c/D2d/D3/D4/D5/D7.

## A. Executive Summary

**PASS WITH FINDINGS برای runtime توسعه.** Publisher، Worker و Recovery پیاده‌سازی شدند و مسیر واقعی PostgreSQL/RabbitMQ تا HTTP completion روی هر دو Broker تأیید شد. اجرای نهایی ماتریس 36 نتیجه package موفق داشت: 18/18 روی RabbitMQ 4.4.1 و 18/18 روی 4.3.8، بدون FAIL یا SKIP. Build، vet، unit و Linux race موفق‌اند.

این نتیجه «PASS بی‌قیدوشرط تمام گیت‌ها» یا مجوز production نیست. GitHub Actions میزبانی‌شده اجرا نشده، چون stage/commit/push مجاز نبود؛ اثبات تازه down/up دیتابیس disposable نیز در workflow تعریف شده اما در این اجرا انجام نشده است. Lint قابل اجراست، ولی 32 finding advisory دارد. جزئیات و محدودیت‌ها در H و K آمده‌اند.

## B. Implemented Components

- **RabbitMQ/config:** dependency دقیق، بارگذاری و validation تنظیمات، reconnect سطح Application، غیرفعال بودن automatic recovery، startup fail-closed و topology/policy verification. Compose دارای image digest، health check، volume پایدار، restart policy و پورت‌های loopback است.
- **Message contract:** پنج فیلد `schema_version`، `organization_id`، `delivery_id`، `dispatch_seq` و `outbox_id`؛ MessageId برابر Outbox ID، CorrelationId برابر Delivery ID و چهار header قراردادی. Event payload و secret در Broker قرار نمی‌گیرند. فقط دو counter افزوده‌شده RabbitMQ، یعنی `x-acquired-count` و `x-delivery-count`، علاوه بر چهار header پذیرفته می‌شوند؛ header کاربردی اضافی همچنان نامعتبر است.
- **Publisher:** T1 commit-before-publish، dedicated confirm channel، N=1، یک listener pair در هر generation، mandatory publication، drain کردن Return پیش و پس از Confirm، نتیجه UNKNOWN و کنارگذاشتن کامل generation، T2 fenced و T3 reaper مستقل از batch کند.
- **Worker:** acquisition دقیق pointer، lease ثابت، زمان تازه دیتابیس، guard بودجه پیش از HTTP، HTTP/TLS و response محدود، retry policy ثابت، completion اتمیک و RB-4. Handle به connection/channel generation خودش تعلق دارد؛ ACK/Reject نسل قبلی روی کانال جدید ارسال نمی‌شود.
- **Recovery:** چهار pass به ترتیب expire → repair → re-arm → stale؛ بودجه و candidate cap، short transaction، SKIP LOCKED، expiry precedence و scheduler فوری سپس fixed-delay، بدون overlap در یک process.
- **Lifecycle/observability:** startup واقعی Worker، readiness پس از ایجاد consumer، structured logs و metrics با نام‌های ثابت، drain محدود و shared shutdown deadline برای componentها، metrics و pool. Watchdog مستقل connection، RPC/socket write مسدود AMQP را بدون انتظار برای mutex کاربردی قطع می‌کند. Timeout cleanup به خطا تبدیل می‌شود، نه موفقیت پنهان.
- **CI:** jobهای quality، ماتریس integration دو Broker، CGO race و lint advisory؛ pinهای Go/action/image و گیت منع SKIP در integration.

D6/U25 پیاده‌سازی نشده‌اند: فقط snapshot توسعه با `secret_encrypted` خالی HTTP unsigned اجرا می‌کند. مقدار غیرخالی قبل از HTTP fail-closed می‌شود و disposition پایدار decrypt-failure می‌گیرد؛ attempt/count افزایش نمی‌یابد.

## C. Architecture Compliance

| تصمیم | انطباق تحویل |
| --- | --- |
| D1a | `github.com/rabbitmq/amqp091-go v1.17.0` در module pin شده؛ API واقعی آن استفاده شده است. |
| D1b | 4.4.1 نسخه اصلی و 4.3.8 compatibility/fallback؛ هر دو با PostgreSQL واقعی تست شدند. انتخاب fallback پنهان انجام نشد. |
| D2b | durable topic `hookflow.dispatch`، routing key `delivery`، properties و identifiers-only body مطابق قرارداد. |
| D2c | فقط یک durable shared quorum queue: `hookflow.dispatch.q`؛ بدون sharding یا فرض tenant fairness. |
| D2d | durable direct `hookflow.dispatch.dlx` و quorum `hookflow.dispatch.dlq`، key=`delivery.failed`، strategy=`at-most-once`، limit=20؛ بدون delayed business retry یا replay خودکار. |
| D3 | batch=200، max claims=5، poll=500ms، lease=30s، reap=10s، exponential full jitter با base=2s و cap=60s؛ bounded confirm protocol و overflow checks. |
| D4 | L/D/H/P/S برابر 30s/10s/2s/3s/10s؛ concurrency/prefetch=24، pool=30، max attempts=8، horizon=12h؛ invariant دقیق `L > D + H + P + S`. |
| D5 | cadence=60s، total=45s، pass budgets=12/10/10/8s، caps=500/250/250/100 و stale=5m؛ همان ترتیب و expiry precedence. |
| D7 | workflow و تست‌های واقعی محلی کامل‌اند؛ اجرای remote CI هنوز unverified است و به‌عنوان موفق ثبت نشده است. |

Acquisition همچنان published-only است. pointer زنده با Outbox در وضعیت `publishing`، برخلاف generation واقعاً stale، HOLD می‌شود تا T2 تعیین تکلیف کند. این اصلاح از ACK زودهنگام جلوگیری می‌کند و قرارداد acquisition یا مالکیت retry را تغییر نمی‌دهد.

## D. Changed Files

`untracked` به معنی «همه این فایل‌ها در این اجرا ساخته شده‌اند» نیست؛ بخش بزرگی از Repository از قبل untracked بود. فهرست زیر فایل‌های ایجادشده یا تکمیل/اصلاح‌شده در اجرای Bundle 1 را بر اساس حوزه نشان می‌دهد. فایل‌های موجود ابتدا بررسی شدند؛ اصلاحات قبلی Owner حفظ شدند.

### Dependency، infrastructure و CI

- [go.mod](E:/hookflow/go.mod)، [go.sum](E:/hookflow/go.sum): pin و checksum کلاینت AMQP؛ نسخه‌های سایر dependencyها بی‌دلیل تغییر نکردند.
- [.env.example](E:/hookflow/.env.example)، [compose.yaml](E:/hookflow/compose.yaml)، [scripts/dev.sh](E:/hookflow/scripts/dev.sh): تنظیمات نمونه، RabbitMQ پایدار و healthy، startup صریح dependencies؛ بدون migration خودکار.
- [.github/workflows/ci.yml](E:/hookflow/.github/workflows/ci.yml)، [.golangci.yml](E:/hookflow/.golangci.yml): D7، ماتریس دو Broker، race و lint advisory.

### Config و platform

- [config.go](E:/hookflow/internal/platform/config/config.go)، [runtime.go](E:/hookflow/internal/platform/config/runtime.go)، [runtime_test.go](E:/hookflow/internal/platform/config/runtime_test.go)، [rabbitmq.go](E:/hookflow/internal/platform/config/rabbitmq.go)، [rabbitmq_test.go](E:/hookflow/internal/platform/config/rabbitmq_test.go)، [metrics.go](E:/hookflow/internal/platform/config/metrics.go)، [metrics_test.go](E:/hookflow/internal/platform/config/metrics_test.go): validation و defaults؛ vhost یک‌بار decode و مطابق parser کلاینت استخراج می‌شود.
- [pool.go](E:/hookflow/internal/platform/database/pool.go)، [pool_test.go](E:/hookflow/internal/platform/database/pool_test.go): pool size مصوب Worker و integer bounds.
- [v7.go](E:/hookflow/internal/platform/id/v7.go)، [v7_test.go](E:/hookflow/internal/platform/id/v7_test.go): UUIDv7 کاربردی.
- [observability/metrics.go](E:/hookflow/internal/platform/observability/metrics.go)، [metrics_test.go](E:/hookflow/internal/platform/observability/metrics_test.go)، [server.go](E:/hookflow/internal/platform/observability/server.go): metrics fixed-name و listener اختیاری.
- [messaging/connection.go](E:/hookflow/internal/platform/messaging/connection.go)، [connection_test.go](E:/hookflow/internal/platform/messaging/connection_test.go)، [connection_failure_integration_test.go](E:/hookflow/internal/platform/messaging/connection_failure_integration_test.go): dial/reconnect، secret-safe errors و watchdogهای RPC/teardown با proxy واقعی TCP.
- [messaging/message.go](E:/hookflow/internal/platform/messaging/message.go)، [message_test.go](E:/hookflow/internal/platform/messaging/message_test.go)، [topology.go](E:/hookflow/internal/platform/messaging/topology.go)، [topology_test.go](E:/hookflow/internal/platform/messaging/topology_test.go): contract سخت‌گیرانه و topology/policy fail-closed.

### Runtime و DB adapters

- [publisher/backoff.go](E:/hookflow/internal/app/publisher/backoff.go)، [decision.go](E:/hookflow/internal/app/publisher/decision.go)، [decision_test.go](E:/hookflow/internal/app/publisher/decision_test.go)، [relay.go](E:/hookflow/internal/app/publisher/relay.go)، [relay_test.go](E:/hookflow/internal/app/publisher/relay_test.go)، [session.go](E:/hookflow/internal/app/publisher/session.go)، [session_failure_integration_test.go](E:/hookflow/internal/app/publisher/session_failure_integration_test.go): bounded Publisher و failure coverage.
- [worker/consumer.go](E:/hookflow/internal/app/worker/consumer.go)، [consumer_random.go](E:/hookflow/internal/app/worker/consumer_random.go)، [consumer_shutdown_test.go](E:/hookflow/internal/app/worker/consumer_shutdown_test.go)، [handle.go](E:/hookflow/internal/app/worker/handle.go)، [handle_test.go](E:/hookflow/internal/app/worker/handle_test.go): QoS، reconnect، نسل Handle و drain محدود.
- [worker/executor.go](E:/hookflow/internal/app/worker/executor.go)، [executor_test.go](E:/hookflow/internal/app/worker/executor_test.go)، [processor.go](E:/hookflow/internal/app/worker/processor.go)، [processor_test.go](E:/hookflow/internal/app/worker/processor_test.go)، [retry.go](E:/hookflow/internal/app/worker/retry.go)، [retry_test.go](E:/hookflow/internal/app/worker/retry_test.go): HTTP، disposition و retry ladder.
- [delivery/pointer.go](E:/hookflow/internal/app/delivery/pointer.go)، [pointer_test.go](E:/hookflow/internal/app/delivery/pointer_test.go)، [pointer_integration_test.go](E:/hookflow/internal/app/delivery/pointer_integration_test.go)، [message.go](E:/hookflow/internal/app/delivery/message.go)، [store.go](E:/hookflow/internal/app/delivery/store.go)، [store_test.go](E:/hookflow/internal/app/delivery/store_test.go): exact acquisition/HTTP preparation، terminal NULL timestamp، pre-T2 retention، Unicode snippet و credential-header redaction.
- [disposition/policy.go](E:/hookflow/internal/app/disposition/policy.go)، [policy_test.go](E:/hookflow/internal/app/disposition/policy_test.go)، [outbox/store.go](E:/hookflow/internal/app/outbox/store.go): اتصال runtime به core موجود و اصلاحات محدود کیفیت؛ RB-4/RB-5 حفظ شدند.
- [recovery/builder.go](E:/hookflow/internal/app/recovery/builder.go)، [scheduler.go](E:/hookflow/internal/app/recovery/scheduler.go)، [scheduler_test.go](E:/hookflow/internal/app/recovery/scheduler_test.go)، [sweep.go](E:/hookflow/internal/app/recovery/sweep.go)، [sweep_test.go](E:/hookflow/internal/app/recovery/sweep_test.go): payload مشترک، scheduler و حفظ core چهار-pass.

### Commands، integration و مستندات

- [cmd/outbox-publisher/main.go](E:/hookflow/cmd/outbox-publisher/main.go)، [main_test.go](E:/hookflow/cmd/outbox-publisher/main_test.go)، [lifecycle.go](E:/hookflow/cmd/outbox-publisher/lifecycle.go)، [lifecycle_test.go](E:/hookflow/cmd/outbox-publisher/lifecycle_test.go): executable Publisher.
- [cmd/worker/entity_id.go](E:/hookflow/cmd/worker/entity_id.go)، [integration_test.go](E:/hookflow/cmd/worker/integration_test.go)، [lifecycle.go](E:/hookflow/cmd/worker/lifecycle.go)، [lifecycle_test.go](E:/hookflow/cmd/worker/lifecycle_test.go): composition، startup و shared shutdown deadline.
- [cmd/api/lifecycle.go](E:/hookflow/cmd/api/lifecycle.go)، [cmd/migrate/main.go](E:/hookflow/cmd/migrate/main.go): اصلاحات محدود lifecycle/کیفیت و توضیحات؛ API ingestion یا migration جدید اضافه نشده است.
- [test/integration/harness_test.go](E:/hookflow/test/integration/harness_test.go)، [publisher_integration_test.go](E:/hookflow/test/integration/publisher_integration_test.go)، [worker_integration_test.go](E:/hookflow/test/integration/worker_integration_test.go)، [safety_integration_test.go](E:/hookflow/test/integration/safety_integration_test.go)، [recovery_e2e_integration_test.go](E:/hookflow/test/integration/recovery_e2e_integration_test.go)، [dlx_unavailable_integration_test.go](E:/hookflow/test/integration/dlx_unavailable_integration_test.go): E2E و failure tests واقعی. Harness از نام اولیه `harness.go` به فایل test منتقل شد؛ نام اولیه تست DLX نیز به نام فعلی تغییر کرد. این تغییر نام‌ها عملیات Git نبودند.
- [README.md](E:/hookflow/README.md)، [overview.md](E:/hookflow/docs/architecture/overview.md)، [runtime-readiness-decisions.md](E:/hookflow/docs/architecture/runtime-readiness-decisions.md)، [database-schema.md](E:/hookflow/docs/architecture/database-schema.md)، [configuration.md](E:/hookflow/docs/operations/configuration.md)، [runbook.md](E:/hookflow/docs/operations/runbook.md) و همین گزارش: تطبیق وضعیت واقعی، دستورها و evidence تاریخ‌دار؛ تاریخچه approval پاک یا بازنویسی نشده است.

`.gitignore` همان تغییر tracked پیش‌موجود را دارد؛ فایل واقعی `.env` بازنویسی نشد. فایل‌های migration و ERD تغییر نکردند. هیچ فایل یا container پروژه دیگر تغییر داده نشد.

## E. Database & Migration

PostgreSQL واقعی **18.7**؛ `schema_migrations` برابر `13|false`. همه 26 فایل up/down موجود با hashهای baseline یکسان‌اند. هیچ migration/schema جدید لازم نبود و هیچ DDL یا rollback تازه روی دیتابیس توسعه اجرا نشد.

تست موجود `TestMigrationRunnerIntegration`، `Up()` را در نسخه از قبل جاری 13 فراخوانی و `changed=false` را بررسی می‌کند؛ هیچ pending migration اعمال نشد. چرخه تازه down-13/up فقط برای دیتابیس disposable در CI تعریف شده و اجرای remote آن هنوز انجام نشده است.

پس از تست‌ها، deliveries، outbox_messages و delivery_attempts هر سه count=0 بودند. هر دو work queue و DLQ تست نیز ready=0، unacknowledged=0 و consumers=0 داشتند. فقط دو Broker تست و anonymous volume متعلق به آن‌ها، به‌همراه فایل credential موقت تست، پاک شدند. PostgreSQL توسعه، named volume آن و تمام containerهای دیگر حفظ شدند. Runtimeها یا Broker تست عمداً به‌عنوان سرویس دائمی روشن باقی نمانده‌اند.

## F. Test Results

| بررسی | دستور/محیط | نتیجه واقعی |
| --- | --- | --- |
| gofmt | `gofmt -l .` | PASS؛ خروجی خالی |
| go vet | `go vet ./...`، Go 1.27.4 | PASS؛ خروجی خالی |
| go build | `go build ./...` | PASS؛ خروجی خالی |
| unit | `go test ./... -count=1` | PASS؛ 17/17 package |
| PostgreSQL integration | کل suite با `-tags=integration -p 1` روی PG18.7 | PASS در هر دو اجرای Broker؛ نسخه 13 و dirty=false |
| RabbitMQ 4.4.1 | `go test -tags=integration ./... -count=1 -v -p 1` | PASS؛ 18/18 package، صفر SKIP |
| RabbitMQ 4.3.8 | همان دستور با connection تنظیم‌شده برای 4.3.8 | PASS؛ 18/18 package، صفر SKIP |
| race detector | Linux، Go1.27.4، CGO_ENABLED=1، `go test -race ./... -count=1` | PASS؛ 17/17 package |
| real TCP RPC race | دو تست WatchConnection/CloseChannel، داخل Linux و شبکه bridge Broker | PASS؛ بدون DATA RACE |
| lint | `golangci-lint v2.15.0 run --timeout 5m` | اجرا شد؛ exit=1 با 32 finding advisory، نه خطای ناسازگاری Go |
| modules | `go mod verify` و `go mod tidy -diff` | همه moduleها تأیید؛ tidy diff خالی |
| Git whitespace | `git diff --check` | PASS |
| Hosted CI | چهار job workflow | اجرا نشده؛ موفقیت remote ادعا نمی‌شود |

تفکیک lint: cyclop=9، gocognit=6، nestif=1، errcheck=16. موارد errcheck به خروجی‌نویسی CLI migration موجود مربوط‌اند؛ config یا severity lint برای تولید PASS تضعیف نشد. Binary lint با Go1.27.3 ساخته شده و روی پروژه Go1.27.4 analysis را اجرا کرد؛ crash یا رد نسخه Go مشاهده نشد.

محدودیت race ویندوز با `CGO_ENABLED=0` پنهان نشد؛ اجرای واقعی Linux با compiler مناسب جایگزین آن شد. تلاش نخست اتصال race-container به پورت loopback میزبان قابل دسترسی نبود؛ با شبکه bridge و آدرس واقعی Broker تست، همان تست‌ها اجرا و موفق شدند.

## G. Failure Scenario Results

| سناریو | شاهد و نتیجه |
| --- | --- |
| Confirm/Return | unitهای correlator و Broker واقعی: Return متناظر بر Confirm مثبت مقدم است؛ مشاهده Confirm-first باعث T2 زودهنگام نمی‌شود. |
| Negative Confirm / listener anomalies | unit coverage، نتیجه UNKNOWN و generation invalidation؛ T2 success مجاز نیست. Negative confirm واقعی Broker به‌صورت مصنوعی ادعا نمی‌شود. |
| Channel/connection loss | تست‌های Session واقعی: بستن کانال پیش از publish و قطع connection بعد از local publish نتیجه UNKNOWN می‌دهد؛ generation جدید mapping قدیمی را نمی‌گیرد. |
| T1/T2/T3 | Claim قبل از publish قابل مشاهده؛ stale tuple zero-row؛ UNKNOWN منتشرشده ثبت نمی‌شود و lease reaper مسیر cleanup را حفظ می‌کند. |
| Duplicate publish | دو pointer واقعی از Session برای یک Outbox؛ دو ACK پس از یک HTTP و یک attempt؛ T2 دوم fenced و بدون mutation. |
| Pointer قبل از T2 | matching Outbox هنوز publishing؛ صفر HTTP، attempt، ACK و reject؛ پس از T2 همان N تکمیل شد، بدون نیاز به ساخت N+1. |
| Stale tag | tag=1 در نسل A و B؛ ACK و Reject Handle A هر دو ErrStaleHandle؛ B unsettled می‌ماند و با shutdown requeue می‌شود. |
| Reject / DLX | 21 acquisition/reject واقعی تحت limit=20؛ DLQ واقعی با pointer صحیح، routing صحیح و x-death بازرسی شد. |
| DLX route unavailable | پیش از اولین reject، DLQ binding با gate کنترل‌شده حذف شد؛ pointer از دست رفت، DLQ افزایش نیافت، Recovery دقیقاً یک N+1 دیتابیس‌محور ساخت؛ binding در cleanup بازگردانده شد. |
| Recovery E2E | T1 → Publish N → lease/reject → DLQ/loss → lease expiry → N+1 → Publish → HTTP → Complete؛ فقط یک attempt موفق نهایی، old N حفظ و sweep دوم بدون N+1 تکراری. |
| Fencing/expiry | تست‌های PostgreSQL برای exact B-not-A، stale completion بدون attempt، lease/event expiry، فعال‌بودن lease و precedence expiry موفق‌اند. |
| Shutdown | HTTP فعال commit/ACK؛ prefetched HOLD بدون mutation requeue؛ شمارنده negative settlement صفر. failure یک component، سایر componentها را cancel و join می‌کند. |
| Blocked AMQP RPC | proxy واقعی TCP، channel.open-ok/close-ok را نگه می‌دارد؛ watchdog با budget=150ms اتصال را آزاد می‌کند؛ روی هر دو Broker و زیر race موفق است. |

شکست‌های حین توسعه حذف/skip نشدند. علت‌های اصلاح‌شده شامل مقایسه spelling JSON به‌جای snapshot JSONB، NULL بودن next_attempt_at روی terminal Delivery، headers افزوده‌شده quorum queue، cleanup نامناسب duplicate UNKNOWN و sampling تأخیردار management بود. تست stale-tag از window کوتاه 2.9s که پیش از sampling به‌درستی abort می‌شد، به window مصوب 30s منتقل شد؛ assertionهای tag، stale Handle، unsettled delivery و requeue حفظ شدند و Processor تست cancellation-aware شد.

## H. Remaining Findings

### گیت‌های بسته‌نشده

1. **Hosted CI:** اجرای Actions و down/up disposable هنوز شاهد موفقیت ندارد. با ممنوعیت commit/push، این بخش در این اجرا قابل انتشار و اجرا نبود. از روی تست محلی، وضعیت remote به Green تغییر داده نشده است.
2. **Production:** D6/U25، API ingestion/authentication/fan-out، SSRF/egress، HA/failover، capacity benchmark و production alert/scrape topology خارج از این Bundle یا نیازمند تصمیم/مجوز بعدی‌اند. Production activation همچنان مسدود است.

### موارد غیرمسدودکننده توسعه

- 32 finding advisory lint؛ موارد پیچیدگی و خروجی‌نویسی CLI، نه شکست build/vet/test یا ناسازگاری نسخه.
- `.env` واقعی دست‌نخورده است؛ برای اجرای دائمی باید تنظیمات RabbitMQ در محیط process با service credentials تطبیق داده شوند. credential موقت تست به configuration دائمی تبدیل نشده است.
- تضمین at-least-once/Recovery، تضمین exactly-once در مقصد نیست؛ receiver باید deduplicate کند. Dead-letter copy ممکن است از دست برود و DLQ replay خودکار وجود ندارد.
- componentی که عمداً cancellation را نادیده بگیرد یا connection دیتابیس را هرگز آزاد نکند، قابل تضمین graceful نیست؛ deadline و خطای cleanup آشکار می‌شوند و executable در مسیر fatal خاتمه می‌یابد. تست‌های مسیر عادی، drain و آزادسازی socket/goroutineهای proxy موفق‌اند.

## I. Runtime Instructions

از ریشه Repository و در Git Bash اجرا شود. فایل `.env` موجود را overwrite نکنید:

```bash
test -f .env || cp .env.example .env
# placeholderها را با مقادیر محلی خود جایگزین کنید؛ URI credentialها URL-encoded باشد.
set -a
. ./.env
set +a
./scripts/dev.sh
go run ./cmd/migrate version
```

پورت‌های نمونه development: PostgreSQL=`5433`، AMQP=`5673`، management=`15673`، همگی loopback. Scripts dependency startup را انجام می‌دهند، نه migration خودکار. برای دیتابیس جدید، **فقط پس از مجوز اجرای migration**:

```bash
go run ./cmd/migrate up
go run ./cmd/migrate version
```

سه terminal با environment یکسان:

```bash
go run ./cmd/api
HOOKFLOW_METRICS_ADDR=127.0.0.1:9191 go run ./cmd/outbox-publisher
HOOKFLOW_METRICS_ADDR=127.0.0.1:9192 go run ./cmd/worker
```

Recovery داخل Worker اجرا می‌شود و executable جداگانه ندارد. پورت‌های metrics اختیاری‌اند و باید آزاد باشند؛ process دیگری روی همان پورت اجرا نشود.

```bash
curl -fsS http://127.0.0.1:8080/health/live
curl -fsS http://127.0.0.1:8080/health/ready
curl -fsS http://127.0.0.1:9191/metrics
curl -fsS http://127.0.0.1:9192/metrics
```

API readiness فقط PostgreSQL را می‌سنجد؛ delivery readiness با metrics Publisher/Worker سنجیده می‌شود. نبود ingestion API یعنی برای اجرای مسیر توسعه باید snapshot/Outbox معتبر موجود باشد؛ fixtureهای integration این داده را خودشان می‌سازند و پاک می‌کنند.

تست‌ها را فقط روی دیتابیس/queue ایزوله و بدون process مصرف‌کننده دیگر اجرا کنید:

```bash
gofmt -l .
go vet ./...
go build ./...
go test ./... -count=1
go mod verify
go mod tidy -diff
go test -tags=integration ./... -count=1 -v -p 1
# Linux با CGO و GCC:
CGO_ENABLED=1 go test -race ./... -count=1
golangci-lint run --timeout 5m
```

برای compatibility، service تست را با image دقیق `rabbitmq:4.3.8-management-alpine` و URI/management مربوط به همان instance اجرا و همان suite را تکرار کنید؛ image اصلی `rabbitmq:4.4.1-management-alpine` است. tag شناور یا replay DLQ به‌عنوان recovery استفاده نشود. Shutdown عادی با SIGINT/SIGTERM؛ از kill اجباری برای drain عادی استفاده نشود.

## J. Git Safety

HEAD قبل و بعد:

```text
c6db32fd50ee8d7beb3108674b4cf46443d2aba4
```

تنها تغییر tracked همان `.gitignore` پیش‌موجود است. `.env.example`، `.gitattributes`، `.github/`، `.golangci.yml`، README، cmd، compose، db، deployments، docs، go.mod/go.sum، internal، scripts و test همچنان untracked هستند؛ این وضعیت قبلاً نیز شامل بخش عمده implementation بود. Staging diff خالی است.

هیچ stage، commit، push، branch، stash، reset، checkout یا Git clean انجام نشد؛ history و index تغییر داده نشدند. فقط Git read-only inspection و filesystem edit/renameهای مجاز انجام شدند. فایل `.env` یا credential وارد staging یا گزارش نشد.

## K. Final Readiness

**توسعه:** مسیر seeded unsigned Publisher/Worker/Recovery واقعاً end-to-end روی PostgreSQL18.7 و هر دو RabbitMQ مصوب آماده و آزموده است. همه تست‌های نهایی بدون skip موفق‌اند. dependencies دائمی را با تنظیمات محلی صحیح طبق I راه‌اندازی کنید؛ containerهای تست پس از verification پاک شده‌اند.

**Final acceptance کامل:** runtime evidence محلی موفق است، اما hosted CI/down-up proof هنوز بسته نشده؛ بنابراین نتیجه PASS بی‌قیدوشرط صادر نمی‌شود. Lint advisory نیز باید در گزارش باقی بماند.

**Production:** آماده یا مجاز نیست. پیش از deployment باید تصمیم‌های باز D6/U25، سطح API، egress/SSRF، HA/capacity/failover و observability production بسته و تأیید و سپس گیت hosted CI اجرا شود. در این اجرا هیچ مجوز production یا approval جدیدی ثبت نشد.

## Sources:

- [RabbitMQ publisher confirms](https://www.rabbitmq.com/docs/confirms)
- [RabbitMQ 4.3 quorum queues و delivery limit](https://www.rabbitmq.com/docs/4.3/quorum-queues)
- [RabbitMQ dead-letter exchanges](https://www.rabbitmq.com/docs/dlx)
- [RabbitMQ HTTP API reference](https://www.rabbitmq.com/docs/http-api-reference)
- [amqp091-go v1.17.0 source](https://github.com/rabbitmq/amqp091-go/tree/v1.17.0)

منابع برای semantics پروتکل‌اند؛ نتیجه PASS تست‌ها از اجرای واقعی محلی به دست آمده، نه صرف مطالعه مستندات.
