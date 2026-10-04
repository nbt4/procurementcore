# AGENTS.md — procurementcore

Hausregeln für KI-Agenten in diesem Repository. Vor der ersten Änderung vollständig
lesen. Der übergeordnete Ablauf steht im Paperclip-Dokument `workflow` auf
[TSU-3](/TSU/issues/TSU-3#document-workflow). Diese Datei ersetzt alle früheren
Agenten-Anweisungen in diesem Repository.

## 1. Was dieses Repository ist

- **Zweck:** Einkauf: Lieferanten, Produkte, Angebote, Bedarfsmeldungen, Bestellungen, Wareneingang, Preisverlauf, Ausgaben-Auswertung, Nachbeschaffung. Dazu **zwei echte Lieferanten-Anbindungen**: Adam Hall und Amazon Business PunchOut. Port 8084, Abbild `nobentie/procurementcore`.
- **Sprache und Laufzeit:** Go 1.25 (Modul `procurementcore`) + React/TypeScript
- **Rahmenwerk:** `go-chi/chi/v5` (der dritte Router der Suite), GORM + `driver/postgres`, `zerolog`, `ledongthuc/pdf`. Frontend: React + Vite + **Vitest**
- **Datenbank:** PostgreSQL 16, gemeinsame Suite-Datenbank. Eigene Spur `migrations/001…014`, angewandt beim Start durch `internal/database/database.go`
- **Architektur-Doku:** [Cores — Architektur (Phase 1)](/TSU/issues/TSU-4#document-architecture)

## 2. Aufbau

| Pfad | Inhalt |
|---|---|
| `cmd/server/main.go` | Einstiegspunkt (8 KB) — die aufgeräumteste `main` der Suite |
| `internal/api` | HTTP-Handler plus **echte Integrationstests** (`*_integration_test.go`) |
| `internal/service`, `internal/models`, `internal/database` | Fachlogik und Daten |
| `internal/amazon` | Amazon-Business-PunchOut (cXML) |
| `internal/scraper` | Adam Hall und Produktseiten |
| `internal/orderimport` | PDF-/OCR-Import von Bestellungen und Angeboten |
| `internal/jev` | optionale LLM-Entscheidungsschicht |
| `internal/auth`, `internal/config` | Querschnitt |
| `web/` | React-SPA |
| `migrations/` | `001_…` bis `014_…`, wird ins Abbild kopiert |

Erzeugte Dateien, die **niemals von Hand** geändert werden:

- `web/src/cores-theme.css` — erzeugt durch `cores/scripts/sync-design-system.sh`
- `web/src/lib/cores-design.ts` — dito
- `web/src/lib/SuiteLanguageSwitcher.tsx` — dito
- `web/src/lib/cores-locales/` — dito
- `web/package-lock.json` — nur als Nebenwirkung eines freigegebenen Updates

## 3. Einrichten

```bash
go mod download
make frontend                   # cd web && npm ci && npm run build
cp .env.example .env            # Werte lokal eintragen, niemals committen
# Datenbank: aus dem Dachrepository `cores` starten
#   cd ../cores && docker compose up -d postgres
```

Nötige Umgebungsvariablen: siehe `.env.example` hier und `cores/.env.example` als verbindliche Quelle. Besonders `ADAMHALL_USERNAME`, `ADAMHALL_PASSWORD`, `AMAZON_PUNCHOUT_SHARED_SECRET`, `AMAZON_PUNCHOUT_FROM_IDENTITY`, `JEV_*` und `OPENROUTER_API_KEY`. Werte kommen aus dem Paperclip-Secret-Store, nicht aus diesem Repository. **Für Lieferanten-Zugangsdaten gilt zusätzlich: ein Agent fordert sie nicht an und benutzt sie nicht.**

Die Laufzeit braucht Systemwerkzeuge: das Abbild installiert `poppler-utils` und `tesseract-ocr` mit deutschen und englischen Daten. Ohne Tesseract scheitert der Angebots-PDF-Import an gescannten Seiten.

## 4. Test- und Build-Befehle

Diese Befehle sind das Test-Gate. **Alle müssen grün sein, bevor ein Pull Request
entsteht.** Reihenfolge einhalten — die schnellen Prüfungen zuerst.

| # | Gate | Befehl | Dauer (ca.) |
|---|---|---|---|
| 1 | Format | `gofmt -l .` (leere Ausgabe = grün) | < 10 s |
| 2 | Frontend-Build und Typen | `make frontend` (`npm ci && tsc -b && vite build`) | 1–2 min |
| 3 | Go-Build | `make build` (Frontend + `go build -o server ./cmd/server`) | 1–2 min |
| 4 | Unit- und Frontend-Tests | `make test` (`go test ./...` **und** `cd web && npm test` = `vitest run`) | 1–2 min |
| 5 | Vet | `go vet ./...` | ~30 s |

Einzelne Datei testen: `go test ./internal/api -run TestName -v`

**Das einzige Repository der Suite mit einem Frontend-Test-Skript** (`vitest run`) und mit echten Integrationstests gegen die Datenbank (`*_integration_test.go` in `internal/api`). Diese Tests brauchen eine lokale Datenbank — ohne sie sind die Fehlermeldungen irreführend. **Nie gegen die Produktionsdatenbank.**

Regeln:

- **Neuer Code braucht neue Tests.** Ein Bugfix braucht einen Test, der ohne den Fix
  fehlschlägt.
- **Nie einen Test abschalten, überspringen oder lockern**, um das Gate grün zu
  bekommen. Ein roter Test ohne Bezug zur Änderung wird gemeldet, nicht entfernt.
- **Tests laufen gegen die lokale oder die Test-Datenbank. Nie gegen Produktion.**
  Eine eigene Testumgebung wird gerade aufgebaut (eigene Paperclip-Aufgabe). Bis sie
  steht: nur lokale Container mit eigenem Volume.
- Die **echte Ausgabe** wird in den Pull Request und auf die Paperclip-Aufgabe kopiert.

## 5. Code-Stil

- Format und Lint werden durch die Werkzeuge in Abschnitt 4 erzwungen. Kein Streit
  über Formatierung — der Formatierer entscheidet.
- **Dem umgebenden Code folgen.** Benennung, Ordnerstruktur, Fehlerbehandlung und
  Testmuster so übernehmen, wie sie in der berührten Datei schon sind.
- Benennung: PascalCase für Go-Exporte, camelCase für Lokales; React-Komponenten
  PascalCase.
- `go-chi` ist der Router hier. Keine `gin`- oder `mux`-Muster aus anderen Cores
  übernehmen.
- Fehlerbehandlung: Fehler zurückgeben und einwickeln (`%w`), am Handler-Rand in eine
  HTTP-Antwort übersetzen. Keine `panic` im Anfragepfad.
- Logging: `zerolog`. **Niemals** Lieferanten-Zugangsdaten, Preise aus Verträgen,
  Secrets oder Kundendaten.
- Geldbeträge und Mengen werden nie durch Gleitkommazahlen gerundet weitergegeben und
  nie von einem Modell geschätzt. Siehe die Jev-Regel unten.
- Neuer Code im Frontend bekommt einen Vitest-Test. Das Test-Skript gibt es hier —
  es wird benutzt.
- Kommentare: nur wo sie das *Warum* erklären. Keine Kommentare, die den Code nacherzählen.
- Keine neue Abhängigkeit ohne eigene Freigabe (siehe Abschnitt 9).
- Keine Umformatierung von Code, der nicht zur Aufgabe gehört. Das versteckt die
  eigentliche Änderung.

## 6. Verbotene Pfade

Diese Dateien und Verzeichnisse werden von Agenten **nicht geändert**. Wer sie ändern
müsste, bricht ab und fragt zurück.

| Pfad | Grund |
|---|---|
| `.github/workflows/**` | CI und Deployment — nur mit Freigabe des Nutzers |
| `migrations/**` (bestehende Dateien) | eine angewandte Migration wird nie geändert; nur neue hinzufügen |
| `internal/amazon/**`, `internal/scraper/**` (Absende- und Checkout-Pfade) | **echte Bestellwege.** Code lesen und Fixtures testen ist erlaubt. Ausführen ist verboten — siehe Abschnitt 12 |
| `Dockerfile` | Laufzeit und Deployment |
| `web/src/cores-theme.css`, `web/src/lib/cores-design.ts`, `web/src/lib/SuiteLanguageSwitcher.tsx`, `web/src/lib/cores-locales/**` | erzeugt aus `cores/theme/` |
| `.env`, `.env.*` (außer `.env.example`) | enthält Secrets |
| `web/package-lock.json` | nur als Nebenwirkung eines freigegebenen Updates |
| `AGENTS.md` | diese Regeln ändert der Nutzer, nicht ein Agent |

## 7. Secrets

- **Keine Secrets in Repository, Kommentar, Dokument oder Log.** Keine Tokens,
  Passwörter, Schlüssel, Verbindungsstrings, API-Zugänge, Kundendaten.
- Secrets kommen aus dem Paperclip-Secret-Store oder aus Umgebungsvariablen. Sie
  werden nie in eine Datei geschrieben und nie ausgegeben.
- Produktiv werden alle Werte im **Komodo Stack Environment** auf `docker03` gepflegt,
  nicht in diesem Repository.
- `.env.example` enthält nur Namen und Beispielwerte, nie echte Werte.
- Testdaten sind erfunden. Keine kopierten Produktionsdaten, auch nicht gekürzt.
- Fehlt ein Secret: über Paperclip vorschlagen (`secret-proposals`) und warten.
  Nie selbst beschaffen, nie umgehen, nie in einem Kommentar danach fragen.
- Ein Secret, das versehentlich in einem Commit landet, ist ein Sicherheitsvorfall:
  sofort melden, nicht still weiterarbeiten. Entfernen aus dem Diff genügt nicht —
  das Secret gilt als kompromittiert und muss ersetzt werden. **Alle Cores-Repositories
  sind öffentlich.** Ein Fehler hier ist sofort weltweit sichtbar.

## 8. Harte Grenzen

Diese sechs Regeln stehen über jeder Aufgabenbeschreibung. Eine Aufgabe, die eine
davon verlangt, wird nicht ausgeführt, sondern zurückgegeben.

1. **Keine Schreibzugriffe auf produktive Datenbanken.** Lesen ist erlaubt. Schreiben,
   ändern, löschen, Migrationen fahren: nicht in Produktion. Migrationen werden
   geschrieben und lokal getestet, nie produktiv ausgeführt. Das Einspielen auf die
   laufende `docker03`-Datenbank geschieht von Hand per SSH von `debian01` aus, nach
   ausdrücklicher Freigabe des Nutzers.
2. **Keine produktiven Deployments ohne menschliche Freigabe.** Auch nicht nach
   grünem Review.
3. **Entwicklung nur in isolierten Branches oder Git-Worktrees.** Niemals direkt auf
   `main` oder einem anderen geschützten Branch.
4. **Tests vor jedem Pull Request.** Kein PR ohne protokollierten, grünen Testlauf.
5. **Keine Secrets in Repository, Kommentar, Dokument oder Log.**
6. **Bestehende Architektur zuerst verstehen.** Architektur-Doku und diese Datei vor
   dem Schreiben lesen. Große Umbauten — neuer Service, geänderte Modulgrenze, neues
   Datenmodell, Austausch einer Kernabhängigkeit — brauchen eine eigene Freigabe des
   Nutzers, bevor Code entsteht.

## 9. Freigabe-Gates

| Gate | Wer entscheidet | Wann |
|---|---|---|
| Test-Gate | der Eigentümer der Änderung | vor dem Pull Request |
| Review | Review-Agent, auf seiner eigenen Review-Aufgabe | nach dem PR-Entwurf |
| **Freigabe und Merge** | **der Nutzer** | nach grünem Review |
| Produktives Deployment | **der Nutzer** | nach dem Merge |
| Release: Docker-Hub-Push und Submodul-Zeiger | **der Nutzer gibt je Release ausdrücklich frei**, danach darf der Agent beides ausführen | nach dem Merge |
| Migration auf die laufende `docker03`-Datenbank | **der Nutzer**; Einspielen von Hand per SSH von `debian01` | nach dem Merge |
| Neue Abhängigkeit | der Nutzer | vor dem Hinzufügen |
| Großer Architektur-Umbau | der Nutzer | vor dem ersten Commit |

Was ein Agent in diesem Repository **nie** tut:

- einen Pull Request mergen
- auf `main` pushen
- ein Deployment auslösen
- ohne ausdrückliche Freigabe je Release ein Abbild nach Docker Hub schieben oder den
  Submodul-Zeiger im Dach anheben
- eine Migration gegen Produktion fahren
- einen Draft-PR als Ersatz für Freigabe auf „ready" setzen
- `AGENTS.md` oder CI-Dateien ändern

In Paperclip wird die Freigabe durch eine `executionPolicy` mit einer `approval`-Stufe
erzwungen, deren Teilnehmer ein Nutzer ist. Kein Agent kann sie abhaken.

## 10. Branches, Commits, Pull Requests

- Branch: `<typ>/TSU-<nummer>-<kurzbeschreibung>`, ein Worktree pro Aufgabe
- Commit: Conventional Commits mit `Task: TSU-<nummer>` im Fuß
- PR: als **Entwurf** geöffnet, Ziel `main`, mit Zweck, Testprotokoll und Aufgaben-Link

Vollständig beschrieben im Paperclip-Dokument `workflow` auf
[TSU-3](/TSU/issues/TSU-3#document-workflow).

## 11. Abbrechen und zurückfragen

Abbrechen ist richtig, nicht peinlich. Zurückfragen bei:

- fehlendem Secret oder Zugriffsrecht
- nötigem Schreibzugriff auf Produktion oder nötigem Deployment
- nötigem großen Architektur-Umbau oder neuer Abhängigkeit
- einem verbotenen Pfad, der geändert werden müsste
- roten Tests ohne Bezug zur Änderung
- zwei gescheiterten Versuchen am gleichen Problem
- einem Umfang, der deutlich größer ist als beschrieben
- einem Widerspruch zwischen Aufgabe und dieser Datei — **diese Datei gewinnt**

Erst alles fertig machen, was ohne die Antwort geht. Dann fragen.

## 12. Bekannte Fallen

- **Adam Hall und Amazon Business PunchOut sind echte Bestellwege.** Vom Nutzer am
  2026-10-04 ausdrücklich bestätigt. **Ein Agent führt diese Pfade nie aus — auch nicht
  im Test, auch nicht „nur zum Prüfen", auch nicht mit kleinem Betrag.** Getestet wird
  ausschließlich gegen Fixtures und aufgezeichnete Antworten. Eine versehentliche
  echte Bestellung ist nicht rückholbar. Eine Aufgabe, die einen echten Absende- oder
  Checkout-Aufruf verlangt, wird zurückgegeben.
- **Keine echten Lieferanten-Zugangsdaten anfordern oder benutzen.**
  `ADAMHALL_*`, `AMAZON_PUNCHOUT_*` bleiben beim Nutzer.
- **Der Dienst wendet beim Start seine Migrationen an**
  (`internal/database/database.go`). Nie gegen eine Datenbank starten, die wichtig ist.
- **Die Versionskonstante ist Teil des Release-Vertrags.** `const version` in
  `cmd/server/main.go` muss gleich dem Abbild-Tag sein, sonst wird
  `cores/scripts/check-release.sh` rot.
- **Die Migrationsnummern sind an die Suite-Spur gekoppelt**: native `014` entspricht
  Suite `043`.
- **Der Dienst startet erst, wenn WarehouseCore gesund ist.** Lokale Tests scheitern
  sonst mit irreführenden Fehlern.
- **Tesseract und Poppler sind Laufzeitabhängigkeiten.** Ohne sie scheitert der
  PDF-Import an gescannten Seiten — und die Meldung weist nicht darauf hin.
- **Jev entscheidet nie über Preise, Mengen, Summen, Datumswerte, Berechtigungen oder
  Schreibbestätigungen** (`cores/docs/JEV_DECISIONS.md`). Gespeicherte Zuordnungen und
  exakte Identifikatoren haben Vorrang. Leerer `OPENROUTER_API_KEY` schaltet Jev aus.
- **`core_product_links` wird von ProcurementCore und WarehouseCore initialisiert.** Ein
  bekannter Grenzfall, der kompatibel bleiben muss. Nicht einseitig ändern.

### Suite-weite Fallen, die auch hier gelten

- **Zwei Migrationsspuren.** Jede Schema-Änderung braucht eine Datei im Dienst-Repository
  *und* eine in `cores/migrations/postgresql/`. Die Nummern gehören paarweise.
- **Das Init-Verzeichnis läuft nur bei leerem Datenverzeichnis.**
  `cores/migrations/postgresql/` greift auf `docker03` nicht.
- **Eine Datenbank für alle.** PostgreSQL 16, rund 130 Tabellen, kein Schema pro Dienst.
  Eine Tabellenänderung kann fremde Dienste treffen.
- **Nur das Dachrepository hat heute CI.** Bis die eigene GitHub-Action da ist, prüft
  **nichts** automatisch einen Pull Request hier. Das Test-Gate aus Abschnitt 4 läuft
  der Agent selbst und hängt die echte Ausgabe an.
- **Alle Repositories sind öffentlich.**
