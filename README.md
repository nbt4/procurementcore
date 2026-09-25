# ProcurementCore

Release `1.0.52` schreibt Entscheidungen zu Bedarfsanforderungen sowie
Wareneingänge mit Vorher-/Nachher-Werten, MCP/KI-Herkunft und Aktivität direkt
in der jeweiligen Datenbanktransaktion. Schlägt der Audit-Eintrag fehl, wird
die gesamte Änderung zurückgerollt. Wareneingänge lehnen nicht endliche oder
übermäßig große Mengen ab.


Release `1.0.51` schützt Bestellanlage und Statuswechsel mit erneuter Prüfung
aktiver Lieferanten/Produkte, Versionssperre, transaktionalem Audit und
dauerhafter Idempotenz. MCP/KI darf Bestellungen nur als Entwurf anlegen;
Versand, Bestätigung und Storno laufen als separate, bestätigte Statuswechsel.
Der Status `sent` verschickt keine Bestellung an einen Lieferanten. Die
Core-API weist rückwärts gerichtete Übergänge ab und setzt beim Anlegen
gelieferte Mengen unabhängig von Eingabedaten auf null.


Release `1.0.50` schützt Bedarfsentwürfe und ihre Einreichung mit
Versionsprüfung unter Datensatzsperre, dauerhafter Idempotenz und
transaktionalem Audit. Produkt- und Lieferantenreferenzen werden unmittelbar
vor der Speicherung erneut geprüft. API-Antworten enthalten die tatsächlich
gespeicherte Version. Die UI kann weiterhin Entwürfe anlegen und einreichen;
MCP/KI-Aufrufe benötigen bei Änderungen `expectedUpdatedAt`. Der isolierte
PostgreSQL-Test läuft mit
`PROCUREMENT_TEST_DATABASE_URL=postgres://.../procurement_test go test ./internal/api`.

Release `1.0.49` schützt Anlage und Änderung von Lieferantenangeboten mit
Versionsprüfung, transaktionalem Audit und dauerhafter Idempotenz. Preisänderungen
schreiben die Preishistorie in derselben Transaktion. `active=false` archiviert
ein Angebot, ohne Historie oder Bestellungen zu löschen.

Release `1.0.48` schützt Produktanlage und Produktänderung durch transaktionales
Audit und dauerhafte Idempotenz. `PUT /products/:id` verlangt bei MCP/KI die
exakte `expectedUpdatedAt`-Version; unter Datensatzsperre werden Kategorie und
offene Bestellungen/Bedarfe vor einer Deaktivierung erneut geprüft. `active=false`
archiviert das Produkt ohne Historienverlust, `active=true` stellt es wieder her.
Ein `initialOffer` wird zusammen mit Produkt und Preishistorie atomar angelegt;
ein ungültiger Lieferant rollt den gesamten Vorgang zurück.
Der isolierte PostgreSQL-Test läuft mit
`PROCUREMENT_TEST_DATABASE_URL=postgres://.../procurement_test go test ./internal/api`.

Release `1.0.47` speichert Kategorien einschließlich vollständigem
Parameter-Schema transaktional mit Aktivität und Audit-Herkunft. MCP/KI-Aufrufe
benötigen einen Idempotenzschlüssel; Änderungen benötigen außerdem die exakte
`expectedUpdatedAt`-Version. Die API gibt nach Änderungen die tatsächlich in
PostgreSQL gespeicherte Version zurück. Ungültige und doppelte Parameterschlüssel
werden abgewiesen. Der isolierte Integrationstest verwendet
`PROCUREMENT_TEST_DATABASE_URL=postgres://.../cores_category_test go test ./internal/api`.

Release `1.0.46` aktualisiert Lieferanten mit einer Sperre und einer für
MCP/KI verpflichtenden `expectedUpdatedAt`-Versionsprüfung. Vorher-/Nachher-
Werte, Aktivität, Audit-Herkunft und Idempotenz-Ergebnis werden atomar
gespeichert. `active=false` deaktiviert einen Lieferanten ohne Löschung und
erscheint im Audit als `supplier.deactivate`; erneutes Aktivieren erscheint als
`supplier.reactivate`. Offene Bestellungen blockieren die Deaktivierung. Die
bestehende UI kann Lieferanten weiter direkt ändern.

Release `1.0.45` speichert die Anlage von Lieferanten zusammen mit Aktivität,
Audit-Herkunft (`UI` oder `MCP/AI`) und bei MCP-Aufrufen dem dauerhaft
gespeicherten Idempotenz-Ergebnis in einer Transaktion. Ein fehlgeschlagener
Audit-Eintrag rollt die Anlage zurück. `active=false` bleibt bei der Anlage
erhalten; ohne Angabe bleibt der Lieferant standardmäßig aktiv.
Der isolierte PostgreSQL-Test läuft mit
`PROCUREMENT_TEST_DATABASE_URL=postgres://.../cores_supplier_test go test ./internal/api`.

Release `1.0.44` liest technische Produktangaben aus üblichen HTML-Mustern
shopübergreifend aus und speichert sie beim Linkimport als Attribute. Tabellen,
Definitionslisten sowie beschriftete Listen und Felder werden berücksichtigt.
JSON-LD und vorhandene Shop-Adapter behalten Vorrang, wenn derselbe Wert bereits
strukturiert vorliegt. Auf der Claypaky-Seite „Tambora Rays“ werden damit unter
anderem Lichtquelle, CRI, DMX/Netzwerkprotokolle, Leistungsaufnahme, Maße und
Gewicht übernommen. Jev wählt weiterhin bei widersprüchlichen Produktkandidaten
das Hauptprodukt; die Jev-Entscheidungs-API liefert selbst keine freien
Attributtexte. Dynamisch erst im Browser geladene oder nur in PDFs enthaltene
Angaben können ohne zusätzliche Datenquelle fehlen.

## Produktlinks mit gemeinsamer Jev-Auswahl (1.0.42)

Der Linkimport sammelt Produktkandidaten aus JSON-LD und schema.org-Microdata
shopübergreifend. Wenn die Seite widersprüchliche Produkte nennt, kann Jev über
OpenRouter anhand von Seitentitel, sichtbarer Überschrift, URL-Pfad und begrenzten
Kandidatenmerkmalen das Hauptprodukt auswählen. Einfache HTML-Seiten verwenden
zusätzlich ihre sichtbare H1-Überschrift. Die vorhandenen Shop-Adapter bleiben
für technische Merkmale und verlässliche Preisquellen erhalten. Bei geringer
Konfidenz oder einem API-Fehler bleibt die lokale Vorschau unverändert. Wird
eine andere Produktidentität gewählt, bleibt der Preis bis zur Prüfung leer;
Jev erhält weder die vollständige Seite noch Preisfelder oder URL-Parameter.
HTTP/2 erlaubt auch den direkten Abruf der Huss-Produktseiten. Gesperrte oder
nur per JavaScript gerenderte Produktseiten können weiterhin eine
eingeschränkte Vorschau liefern.

## Gemeinsames Etikettenbogen-Vokabular (1.0.41)

Die suiteweiten Deutsch-/Englisch-Ressourcen enthalten jetzt auch A4-
Etikettenbögen, Papieroptionen, individuelle Stückzahlen und dynamische
Druckmeldungen des WarehouseCore-Druckcenters.

## Gemeinsames Datentransfer-Vokabular (1.0.40)

Die synchronisierten Deutsch-/Englisch-Ressourcen enthalten jetzt auch die
suiteweit verwendeten Datensatz-, Feld-, Vorschau- und Konfliktbegriffe des
zentralen Import-/Export-Arbeitsbereichs. Fachliche Nutzdaten und technische
Spaltenschlüssel bleiben dabei unverändert.

## Vollständige Dashboard-Lokalisierung (1.0.39)

Die gemeinsame Sprachlogik verarbeitet deutsche und englische Quelltexte nun
bidirektional und ersetzt dynamische Platzhalter. Einkaufskennzahlen,
Prioritäten, Schnellaktionen und Beschaffungsablauf erscheinen damit
vollständig in der gewählten Suite-Sprache.
## Abgesicherte MCP-Freigaben und Wareneingänge (1.0.38)

ProcurementCore unterstützt jetzt die eng begrenzten MCP-Lifecycle-Prozesse aus
Cores MCP 1.5.0. Bedarfsentscheidungen erzwingen das Vier-Augen-Prinzip und
akzeptieren neben Genehmigung und Ablehnung auch eine begründete Rückgabe.
Entscheidungen und Wareneingänge prüfen die in der Vorschau gelesene
`expectedUpdatedAt`-Version. MCP/KI-Aufrufe benötigen außerdem einen
`Idempotency-Key`; Schlüssel, Payload-Hash und Antwort werden in derselben
Transaktion wie die Fachänderung gespeichert.

Wareneingänge unterstützen Teil-, Voll- und ausdrücklich markierte
Überlieferungen. Für einzeln verfolgte Artikel ist pro Gerät eine eindeutige
Seriennummer erforderlich. Bestand beziehungsweise Geräte und ein offener
Putaway-Task entstehen atomar mit Receipt und Bestellstatus. Aktivitäten tragen
die Herkunft `MCP/AI`, während Browser-Operationen als `UI` gekennzeichnet
werden.

## Jev-gestützter Produktabgleich (1.0.37)

Die editierbare PDF-Bestellvorschau kann zuvor nicht erkannte Positionen mit
Jev über OpenRouter gegen den Procurement-Katalog entscheiden. Der
Procurement-/Warehouse-Abgleich nutzt dieselbe Entscheidungsschicht nur zum
Neuordnen unverknüpfter Kandidaten; die eigentliche Verknüpfung bleibt eine
bewusste Benutzeraktion. Exakte SKU-/Alias-Treffer haben weiterhin Vorrang.
Ohne `OPENROUTER_API_KEY`, bei Timeout oder unterhalb
`JEV_MIN_CONFIDENCE` bleibt das bestehende deterministische Ergebnis erhalten.
An OpenRouter gehen nur die einzelne Positionsbeschreibung und begrenzte
Kandidatenstammdaten, niemals die vollständige PDF.

## Deutsch und Englisch (1.0.36)

Die Sidebar bietet die gemeinsame Cores-Sprachwahl. Einkaufsnavigation, häufige
Aktionen, Tabellen- und Formularbegriffe sowie zugängliche Beschriftungen werden
auf Englisch oder Deutsch dargestellt; die Auswahl bleibt beim Core-Wechsel
erhalten.

## Robuster Datenbankstart (1.0.35)

Die GORM-Modelle behandeln die bereits in den SQL-Basismigrationen vorhandenen
Eindeutigkeitsregeln jetzt als PostgreSQL-Constraints. Dadurch bleiben frische
Umbrella-Datenbanken beim automatischen Schemaabgleich unverändert und der
Dienst startet ohne manuelles Löschen seiner Tabellen.

## Bestellungen aus PDF nacherfassen (1.0.34)

Administratoren können bereits getätigte Bestellungen über eine PDF mit
Textebene nacherfassen. ProcurementCore erkennt vorhandene Lieferanten und
Katalogartikel sowie Bestellnummer, Bestell- und Lieferdatum, Währung, Mengen
und Preise. Vor der Anlage zeigt ein vollständig editierbarer Prüfschritt den
Erkennungsgrad, Dokument- und Positionssumme sowie konkrete Unsicherheiten.
Importierte Bestellungen werden als „Gesendet“ oder „Bestätigt“ angelegt; die
hochgeladene PDF wird ausschließlich zur Analyse verarbeitet und nicht
gespeichert. Uploads sind auf 12 MB, 100 Seiten und maschinenlesbare PDFs
begrenzt.

## Adam-Hall-Warenkorb im Shop öffnen (1.0.33)

Nach dem erfolgreichen Aufbau eines Adam-Hall-Warenkorbs kann dieser direkt im
offiziellen Adam-Hall-Shop geöffnet werden. Der Link öffnet einen neuen Tab und
weist darauf hin, dass dort gegebenenfalls die Anmeldung am selben
Geschäftskonto erforderlich ist; serverseitige Zugangsdaten bleiben geschützt.

## Zuverlässiger Adam-Hall-Warenkorb (1.0.32)

Jede Vorschau ersetzt jetzt den bestehenden Adam-Hall-Warenkorb, statt neue
Mengen auf möglicherweise vorhandene Konto-Positionen zu addieren. Anschließend
prüft ProcurementCore jede Artikelnummer und Menge exakt, bevor eine Bestellung
freigegeben werden kann. Die Content Security Policy erlaubt außerdem die vom
gemeinsamen Designsystem geladenen Inter- und JetBrains-Mono-Schriften.

## Adam-Hall-Warenkorb und Direktbestellung (1.0.31)

Freigegebene Bedarfe öffnen nach der Umwandlung mit einem Adam-Hall-Lieferanten
automatisch einen serverseitigen Live-Warenkorb; bestehende Bestellungsentwürfe
bieten dieselbe Aktion. Vor der verbindlichen Bestellung werden Geschäftskonto,
Lieferadresse, Versand- und Zahlungsart, Positionen sowie der aktuelle
Gesamtpreis angezeigt. Nach erfolgreicher Übertragung speichert ProcurementCore
die Adam-Hall-Bestellnummer und finalen Preise. Bleibt die Antwort beim
Absenden technisch uneindeutig, verhindert der Status „Übertragung prüfen“ eine
versehentliche Doppelbestellung, bis das Adam-Hall-Konto kontrolliert wurde.

## Fortlaufender Wareneingang und Bestellreferenz (1.0.30)

Nach einem gebuchten Positionseingang bleibt der Wareneingangsdialog geöffnet
und wechselt bei einer vollständig gebuchten Zeile zur nächsten offenen Position.
Damit lassen sich mehrere Positionen ohne erneutes Öffnen bearbeiten. Zusätzlich
lässt sich die Bestellnummer des Lieferanten bei der Anlage und jederzeit später
im Bestelldialog pflegen; die interne `PO-…`-Nummer bleibt als stabile Cores-ID
erhalten.

## Wareneingang und Warehouse-Bestand (1.0.29)

Wareneingänge für verknüpfte Katalogartikel aktualisieren WarehouseCore in
derselben Datenbanktransaktion: Bei Mengenverfolgung wird der noch keinem
Lagerplatz zugeordnete Bestand erhöht, bei Einzelverfolgung wird je empfangenem
Stück ein Device angelegt. Fehlt die Verknüpfung, bietet der Wareneingangsdialog
passende bestehende Warehouse-Produkte sowie die vorausgefüllte Neuanlage in
WarehouseCore an und lässt sich danach direkt erneut prüfen. Freitextpositionen
bleiben ohne automatische Inventarisierung buchbar.

## Sitzungsprüfung

Cookie- und Bearer-Zugriffe verwenden `cores-common v1.2.0`, um den aktuellen
Kontostatus und die Administratorrolle pro Anfrage zu prüfen. Gesperrte oder
gelöschte Konten werden unmittelbar abgewiesen; Rollenänderungen gelten auch
für bestehende Tokens. Datenbankfehler verweigern Zugriff.

## Einheitliches Cores Designsystem

ProcurementCore verwendet das verbindliche Designsystem aus [`nbt4/cores`](https://github.com/nbt4/cores/blob/main/docs/DESIGN_SYSTEM.md). Die ehemalige separate Einkaufsakte-Palette wurde zugunsten der gemeinsamen Inter-Typografie, Graphitflächen, roten Primärfarbe, 256/80-px-Sidebar sowie identischer Tabellen-, Formular-, Dropdown-, Scrollbar- und Dashboard-Regeln abgelöst.

`web/src/cores-theme.css` und `web/src/lib/cores-design.ts` sind generierte Kopien der Umbrella-Quellen und werden nie direkt geändert. Vor einer Veröffentlichung sind `./scripts/sync-design-system.sh` und `./scripts/check-design-system.sh` im Umbrella-Repository sowie Test und Build dieses Webclients auszuführen.

ProcurementCore ist der Einkaufs-Service des Cores-Ökosystems. Er verbindet Bedarfsmeldungen, Lieferanten, parametrisierbare Artikel, Bezugsquellen, Tiefpreis-Alarme, Freigaben, Bestellungen und Wareneingänge in einem durchgängigen Prozess.

## Funktionsumfang

- Sortierbarer tabellarischer Artikelkatalog mit bearbeitbaren Kategorien, visuellem Parameter-Editor und kontextueller Suche über Artikel, Marke, Modell, SKU, Lieferant, Angebote und beliebige JSON-Parameter ohne JSON-Eingabe
- Sicheres Löschen ungenutzter Kategorien; verwendete Kategorien bleiben gegen Datenverlust geschützt
- Artikelimport aus Produktlinks mit prüfbarer Vorschau, direkt editierbaren Kategorieparametern und automatischer Vorbelegung aus JSON-LD, schema.org-Microdata und OpenGraph; eigene Adapter für Adam Hall, LTT, Huss Licht & Ton, Thomann, Steinigke, ab-in-die-BOX, Caseman und aweo
- Lieferantenstamm mit Preferred-Status, Konditionen, Lieferzeit, Bewertung und Risiko
- Mehrere Angebote pro Artikel mit Einkaufslink, Mindestmenge, Packgröße und Preisverlauf
- Tiefpreis-Alarme, die bei neuen oder geänderten Angeboten automatisch auslösen
- Bedarfsmeldungen mit Entwurf, Einreichung, Vier-Augen-Freigabe, Ablehnung, begründeter Rückgabe, Bestellkonvertierung und direkten Links von Katalogpositionen zum Artikel sowie zur hinterlegten Produktseite
- Nachträgliche Bestellerfassung aus maschinenlesbaren PDFs mit automatischer Lieferanten-, Metadaten-, Positions- und Katalogerkennung sowie editierbarer Prüfung vor dem Speichern
- Optionaler Jev-Entscheidungsabgleich für noch offene PDF-Positionen und Warehouse-Kandidaten, mit Confidence-Schwelle und deterministischem Fallback
- Server-seitig erzeugte Adam-Hall-Warenkörbe für freigegebene Bedarfe und Bestellungsentwürfe: Konto, Lieferadresse, Zahlungsart, Positionen und Live-Gesamtpreis werden vor der verbindlichen Übertragung geprüft; der Warenkorb lässt sich zusätzlich im offiziellen Shop öffnen, und die Adam-Hall-Bestellnummer sowie finalen Preise fließen zurück in ProcurementCore
- Produktabgleich mit WarehouseCore: bestehende Artikel werden anhand EAN/GTIN, Herstellerartikelnummer, Modell, Hersteller und Name vorgeschlagen und anschließend eindeutig verknüpft
- Direkte Übernahme eines Procurement-Artikels in den vollständigen Warehouse-Produktdialog; erkannte Stammdaten und technische Attribute sind vorausgefüllt, bleiben aber bearbeitbar
- Direktbestellungen, Lieferstatus sowie Teil-, Komplett- und ausdrücklich bestätigte Überlieferungen mit transaktionaler Warehouse-Bestands-/Gerätebuchung und Putaway-Task für verknüpfte Artikel
- Spend-, Einsparungs- und Aktivitätsübersicht sowie CSV-Export
- Gemeinsames Cores-SSO über `cores_token` mit zentralem Login, validiertem Rücksprung zur zuvor geöffneten Procurement-Ansicht und serviceübergreifendem Logout
- Zentrales Branding und responsive, dunkel gehaltene Cores-Oberfläche für Desktop und Mobilgeräte
- Vollständiger ProcurementCore-Logosatz für helle/dunkle Flächen, kompakte Navigation, Login, Favicon und dynamisches PWA-Manifest über `/api/v1/branding`
- Ein-/ausklappbare Desktop-Sidebar mit normierter Logo-/Symbolfläche, logofreiem App-Header und reinem Produkt-Favicon im Browser-Tab

## Oberfläche

Das Procurement-Theme folgt einer bewusst sachlichen „Einkaufsakte“-Richtung: Graphit- und Petrolschwarz bilden die Arbeitsfläche, das gemeinsame RentalCore-Rot markiert aktive Navigation und primäre Aktionen. Flache Hierarchien, kompakte Tabellen, durchgehende Kennzahlen und eckige Statusmarker ersetzen dekorative Verläufe, Glows und austauschbare Kartenraster. Die Designregeln und Recherchebasis sind in [`docs/DESIGN.md`](docs/DESIGN.md) dokumentiert.

## Entwicklung

Voraussetzungen: Go 1.25, Node.js 22 und PostgreSQL 16.

```bash
cp .env.example .env
cd web && npm ci && npm run build
cd .. && go test ./...
go run ./cmd/server
```

Der Service läuft standardmäßig auf Port `8084`. `CORES_JWT_SECRET` und die PostgreSQL-Zugangsdaten müssen denen des Cores-Stacks entsprechen. `WAREHOUSECORE_PUBLIC_URL` steuert die serviceübergreifenden Links zum Warehouse-Produktstamm.

Jev ist optional. `OPENROUTER_API_KEY` aktiviert die Entscheidungsschicht auch
für den Produktlinkimport;
`JEV_MODEL` ist standardmäßig `typesafe/jev-1.13`, `JEV_TIMEOUT` auf `3s` und
`JEV_MIN_CONFIDENCE` auf `0.70` gesetzt. `JEV_ENABLED=false` deaktiviert sie
explizit. Der Key gehört ausschließlich in die Laufzeitumgebung oder einen
Secret-Store und niemals ins Repository.

Der Linkimport ruft ausschließlich öffentliche HTTP(S)-Ziele auf Standardports ab, begrenzt Laufzeit und Weiterleitungen und blockiert interne, lokale sowie Link-Local-Netze. Von sehr großen Shopseiten werden höchstens die ersten 16 MB verarbeitet; liegen die Produktdaten wie üblich früh im Dokument, funktioniert der Import auch bei insgesamt größeren Seiten. Neben JSON-LD und OpenGraph verarbeitet der Import schema.org-Microdata und die vom Server deklarierte HTML-Zeichenkodierung. Eigene Shop-Adapter übernehmen bei LTT, Huss Licht & Ton, Thomann und Steinigke Artikelnummern, Marken, Preise und technische Tabellen; ab-in-die-BOX wird für Euroboxen direkt aus der sichtbaren Buybox gelesen, damit fehlerhafte Zubehörpreise im JSON-LD nicht in den Katalog gelangen. Caseman und aweo werden als Casebau-Quellen erkannt. Adam-Hall-Shopseiten werden zusätzlich anhand ihrer serverseitig gerenderten Artikeldaten erkannt. Sind `ADAMHALL_USERNAME` und `ADAMHALL_PASSWORD` als Laufzeit-Secrets gesetzt, führt ProcurementCore den offiziellen Azure-B2C-PKCE-Login ausschließlich serverseitig aus, ergänzt kundenspezifische Preise und baut aus Adam-Hall-Bestellungsentwürfen einen prüfbaren Live-Warenkorb auf. Die Zugangsdaten und der Shopware-Kontext verlassen dabei nie den Server; erst die ausdrücklich bestätigte Aktion „Verbindlich bestellen“ erzeugt die Lieferantenbestellung. Blockiert STE᙭24 den direkten HTML-Abruf mit einer Cloudflare-Challenge, verifiziert ProcurementCore den Produktlink gegen die offizielle STE᙭24-Sitemap und erzeugt aus der kanonischen Produkt-URL eine als eingeschränkt gekennzeichnete Vorschau mit Artikelnummer, Hersteller und erkennbaren Variantenmerkmalen; nicht öffentlich verfügbare Werte wie der Preis bleiben leer. Alle erkannten Originalattribute werden unabhängig von einer Kategorie am Artikel gespeichert, in der Detailansicht angezeigt und von der Katalogsuche berücksichtigt; passende Kategoriefelder werden zusätzlich als typisierte Parameter geführt. JavaScript-only-Shops oder andere Seiten mit Bot-Schutz können unvollständige Daten liefern; alle erkannten Werte bleiben deshalb vor dem Import editierbar.

Der optionale Live-Smoke-Test prüft die öffentlich erreichbaren Beispielartikel aller unterstützten Händler:

```bash
SHOP_SCRAPER_LIVE_TEST=1 go test ./internal/scraper -run TestEventTechnologyShopsLive -v
```

## Container

```bash
docker build -t nobentie/procurementcore:latest .
docker run --rm -p 8084:8084 --env-file .env nobentie/procurementcore:latest
```

Im Gesamt-Stack läuft ProcurementCore als eigener Compose-Service auf Host-Port `8084`. Dasselbe Image arbeitet im globalen Subdomainmodus unter seiner `PROCUREMENTCORE_PUBLIC_URL` oder im Pfadmodus hinter dem Dashboard-Gateway unter `/procurementcore/`.

## API

Alle fachlichen Endpunkte liegen unter `/api/v1` und erwarten das gemeinsame SSO-Cookie oder einen Bearer-Token. Wichtige Ressourcen sind `/products`, `/product-links`, `/suppliers`, `/alerts`, `/requisitions`, `/orders`, `/dashboard` und `/export/spend.csv`. `PUT /suppliers/:id` verlangt für MCP/KI `expectedUpdatedAt` und setzt `active=false` ohne Löschung; Anlage und Änderung schreiben ein transaktionales Audit. Über `/products/:id/warehouse-link` werden bestehende Produkte verknüpft oder wieder getrennt. Admins prüfen mit `POST /orders/:id/adam-hall/cart` einen serverseitigen Live-Warenkorb und übertragen ihn mit `POST /orders/:id/adam-hall/order` verbindlich; zulässig sind ausschließlich Entwürfe eines eindeutig erkannten Adam-Hall-Lieferanten mit ganzzahligen Katalogpositionen. `POST /requisitions/:id/decision` erzwingt für MCP/KI die Version und generell einen anderen Entscheider als den Anforderer. `POST /orders/:id/receipt` sperrt Bestellung und Position, dokumentiert den Eingang und aktualisiert bei verknüpften Artikeln atomar Mengenbestand oder Devices sowie den Putaway-Task. `GET /health` und `GET /api/v1/branding` sind öffentlich.

## Datenhaltung

ProcurementCore verwendet die gemeinsame PostgreSQL-Instanz. Die Tabellen werden beim Start idempotent migriert; die prüfbare SQL-Basis liegt unter `migrations/`. `core_product_links` hält die eindeutige Zuordnung zwischen den eigenständig gepflegten Produktstämmen. Stammdaten bleiben in ihrem jeweiligen Core; nur ein ausdrücklich verbuchter Wareneingang schreibt den zugeordneten Warehouse-Bestand in derselben Transaktion fort. Die Receipt-Felder `warehouse_product_id`, `warehouse_tracking_mode` und `warehouse_quantity_applied` sichern die Zuordnung für das Audit. `proc_idempotency_records` speichert ausschließlich Benutzer, Operation, gehashte Schlüssel/Payloads und die Replay-Antwort; der Klartextschlüssel wird nie persistiert. Geldwerte werden überall als Integer-Cent gespeichert.
