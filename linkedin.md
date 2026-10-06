# LinkedIn post

## Post

Paste everything between the lines as the post body. It is 2,478 characters, inside LinkedIn's 3,000 limit.

---

I saw a Lead SDET role recently and read the spec twice.

Quality strategy, building a team from scratch, shift-left: all things I've done for years. Then: test frameworks in Go. Testing gRPC APIs. High-throughput backend services.

I'd never written Go. I'd never tested gRPC.

I didn't apply. But I didn't want to leave the gap sitting there either, so I built something to close it.

results-service is a Go backend that ingests test results over gRPC and REST, stores them in Postgres and feeds a Grafana dashboard. The service was the vehicle. The testing was the point.

What I learned:

• Go's standard library goes a long way. Routing, structured logging, graceful shutdown and fuzzing, with no frameworks.

• One protobuf file can drive both gRPC and REST. buf's lint rules rejected my first API design: every call needs its own request and response type. Annoying for ten minutes, right forever.

• A test double is only worth having if it behaves like the real thing. I ran one contract suite against both my in-memory store and Postgres, and it caught them disagreeing straight away.

• Commit the tests failing, then make them pass. The history shows red, then green.

• Failure testing is where the bugs live. Pulling the database out from under a running server showed outages coming back as HTTP 500, "server bug", instead of 503, "try again".

• Scale changes everything. With a million results, listing runs took 550 ms because the query counted every result before keeping one page. After the fix: 2 ms.

• Frameworks have opinions. grpc-gateway answers a wrong HTTP method with 501; HTTP says 405. A test caught it.

And some Linux lessons I didn't expect:

• Docker creates missing bind-mount folders as root. Hello, padlock icon.
• go install puts tools in ~/go/bin, which isn't on your PATH by default.
• Inside a container, Git refuses a repo owned by another user, and takes go build down with it.

The result: 157 tests across unit, contract, integration, API, reliability, fuzz and k6 load. Six CI jobs on every push, finishing with a load test against the full Docker stack. Every problem I hit is in the README's issues log, because the bugs you find are the interesting part.

Did I close the gap? I can now write Go, test a gRPC API properly, and explain why each design decision was made. That's a long way from where I was when I read that spec.

Repo link in the comments.

#golang #gRPC #TestAutomation #QualityEngineering #SoftwareTesting #k6

---

## Images

LinkedIn shows attached images as a carousel under the post, so the first one does the most work. Attach them in this order.

| # | Image | What it shows | Status |
| --- | --- | --- | --- |
| 1 | `docs/dashboard.png` | The Grafana dashboard during a k6 soak run | In the repo |
| 2 | `docs/ci-pipeline.png` | The GitHub Actions run graph: lint and proto, then test, integration, build and load-smoke, all green | To take: Actions → latest run → the job graph at the top |
| 3 | `docs/k6-baseline.png` | The k6 summary from `make load`, with per-endpoint p95 for REST and gRPC and every check passed | To take: terminal at the end of a `make load` run |
| 4 | `docs/issues-log.png` | The README's issues and fixes log table | To take: the rendered README on GitHub, scrolled to the log |

If you want a Linux lesson in the carousel and still have a screenshot of the padlocked `deploy` folder, it works well as a fifth image next to the `sudo chown` fix.

## First comment

LinkedIn tends to show posts with links in the body to fewer people, so put the link in the first comment instead:

> Repo, with the full write-up, every test layer and the issues log: https://github.com/adamsjoe/results-service
