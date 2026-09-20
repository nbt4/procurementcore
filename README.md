# ProcurementCore

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
- Bedarfsmeldungen mit Entwurf, Einreichung, Freigabe, Ablehnung, Bestellkonvertierung und direkten Links von Katalogpositionen zum Artikel sowie zur hinterlegten Produktseite
- Nachträgliche Bestellerfassung aus maschinenlesbaren PDFs mit automatischer Lieferanten-, Metadaten-, Positions- und Katalogerkennung sowie editierbarer Prüfung vor dem Speichern
- Server-seitig erzeugte Adam-Hall-Warenkörbe für freigegebene Bedarfe und Bestellungsentwürfe: Konto, Lieferadresse, Zahlungsart, Positionen und Live-Gesamtpreis werden vor der verbindlichen Übertragung geprüft; der Warenkorb lässt sich zusätzlich im offiziellen Shop öffnen, und die Adam-Hall-Bestellnummer sowie finalen Preise fließen zurück in ProcurementCore
- Produktabgleich mit WarehouseCore: bestehende Artikel werden anhand EAN/GTIN, Herstellerartikelnummer, Modell, Hersteller und Name vorgeschlagen und anschließend eindeutig verknüpft
- Direkte Übernahme eines Procurement-Artikels in den vollständigen Warehouse-Produktdialog; erkannte Stammdaten und technische Attribute sind vorausgefüllt, bleiben aber bearbeitbar
- Direktbestellungen, Lieferstatus sowie Teil- und Komplettwareneingänge mit transaktionaler Warehouse-Bestandsbuchung für verknüpfte Artikel
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

Alle fachlichen Endpunkte liegen unter `/api/v1` und erwarten das gemeinsame SSO-Cookie oder einen Bearer-Token. Wichtige Ressourcen sind `/products`, `/product-links`, `/suppliers`, `/alerts`, `/requisitions`, `/orders`, `/dashboard` und `/export/spend.csv`. Über `/products/:id/warehouse-link` werden bestehende Produkte verknüpft oder wieder getrennt. Admins prüfen mit `POST /orders/:id/adam-hall/cart` einen serverseitigen Live-Warenkorb und übertragen ihn mit `POST /orders/:id/adam-hall/order` verbindlich; zulässig sind ausschließlich Entwürfe eines eindeutig erkannten Adam-Hall-Lieferanten mit ganzzahligen Katalogpositionen. `POST /orders/:id/receipt` sperrt die Bestellposition, dokumentiert den Eingang und aktualisiert bei verknüpften Artikeln atomar Mengenbestand oder Devices in WarehouseCore. `GET /health` und `GET /api/v1/branding` sind öffentlich.

## Datenhaltung

ProcurementCore verwendet die gemeinsame PostgreSQL-Instanz. Die Tabellen werden beim Start idempotent migriert; die prüfbare SQL-Basis liegt unter `migrations/`. `core_product_links` hält die eindeutige Zuordnung zwischen den eigenständig gepflegten Produktstämmen. Stammdaten bleiben in ihrem jeweiligen Core; nur ein ausdrücklich verbuchter Wareneingang schreibt den zugeordneten Warehouse-Bestand in derselben Transaktion fort. Die Receipt-Felder `warehouse_product_id`, `warehouse_tracking_mode` und `warehouse_quantity_applied` sichern die Zuordnung für das Audit. Geldwerte werden überall als Integer-Cent gespeichert.
