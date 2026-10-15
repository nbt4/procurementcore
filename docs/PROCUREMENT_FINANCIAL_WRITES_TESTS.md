# Prüfprotokoll: Procurement-Finanzrechte

2026-10-15; isolierter Worktree aus GitHub-main, ausschließlich synthetische
Daten in lokalem PostgreSQL 16 mit eigener `_test`-Datenbank. Keine produktiven
Geschäftsdaten gelesen oder geändert. Alle Befehle endeten mit Exit 0.

## go test -count=1 ./... (PROCUREMENT_TEST_DATABASE_URL gesetzt)

```text
ok  	procurementcore/cmd/server	0.025s
ok  	procurementcore/internal/amazon	0.031s
ok  	procurementcore/internal/api	42.161s
ok  	procurementcore/internal/auth	0.022s
ok  	procurementcore/internal/config	0.018s
ok  	procurementcore/internal/database	7.156s
ok  	procurementcore/internal/jev	0.036s
ok  	procurementcore/internal/models	0.021s
ok  	procurementcore/internal/orderimport	0.014s
ok  	procurementcore/internal/scraper	0.266s
ok  	procurementcore/internal/service	0.009s
```

## go test -race -count=1 ./internal/auth ./internal/api -timeout 180s (Test-URL gesetzt)

```text
ok  	procurementcore/internal/auth	1.032s
ok  	procurementcore/internal/api	48.699s
```

## Weitere Prüfungen

- `go vet ./...`: Exit 0, keine Ausgabe.
- `go build ./cmd/server`: Exit 0, keine Ausgabe (temporäres Ausgabebinary).
- Frontend in Node 22: `npm ci`, `npm test`, `npm run build`: Exit 0;
  7 Vitest-Dateien, 29 Tests erfolgreich; Vite-Build erfolgreich.
- `git diff --check`: Exit 0.
- Unabhängiges Review ohne verbleibende blockierende Befunde; Cookie-/Claim-
  Klassifikation und alle 21 Workflows gezielt nachgeprüft.

HTTP-Auth-Matrizen verweigern fehlende, falsche und falsch typisierte
Finanzgrants bereits bei nil-DB/nil-Scraper vor SQL, externen Aufrufen und Belegen.
Leere Cookie plus Bearer ohne Origin bleibt delegiert. Native UI-Tokens sind
separat erhalten; unguided Import-/Alarm-/Punchout-Wege sind für MCP gesperrt.
PostgreSQL prüft atomare Produkt-/Angebotsanlage, unveränderten gültigen Replay
und verweigerten Replay nach Finanzrechteentzug ohne zusätzliche Daten/Audits.
