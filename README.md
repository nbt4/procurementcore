# ProcurementCore

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
- Produktabgleich mit WarehouseCore: bestehende Artikel werden anhand EAN/GTIN, Herstellerartikelnummer, Modell, Hersteller und Name vorgeschlagen und anschließend eindeutig verknüpft
- Direkte Übernahme eines Procurement-Artikels in den vollständigen Warehouse-Produktdialog; erkannte Stammdaten und technische Attribute sind vorausgefüllt, bleiben aber bearbeitbar
- Direktbestellungen, Lieferstatus, Teil- und Komplettwareneingänge
- Spend-, Einsparungs- und Aktivitätsübersicht sowie CSV-Export
- Gemeinsames Cores-SSO über `cores_token` mit zentralem Login und serviceübergreifendem Logout
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

Der Linkimport ruft ausschließlich öffentliche HTTP(S)-Ziele auf Standardports ab, begrenzt Laufzeit und Weiterleitungen und blockiert interne, lokale sowie Link-Local-Netze. Von sehr großen Shopseiten werden höchstens die ersten 16 MB verarbeitet; liegen die Produktdaten wie üblich früh im Dokument, funktioniert der Import auch bei insgesamt größeren Seiten. Neben JSON-LD und OpenGraph verarbeitet der Import schema.org-Microdata und die vom Server deklarierte HTML-Zeichenkodierung. Eigene Shop-Adapter übernehmen bei LTT, Huss Licht & Ton, Thomann und Steinigke Artikelnummern, Marken, Preise und technische Tabellen; ab-in-die-BOX wird für Euroboxen direkt aus der sichtbaren Buybox gelesen, damit fehlerhafte Zubehörpreise im JSON-LD nicht in den Katalog gelangen. Caseman und aweo werden als Casebau-Quellen erkannt. Adam-Hall-Shopseiten werden zusätzlich anhand ihrer serverseitig gerenderten Artikeldaten erkannt. Sind `ADAMHALL_USERNAME` und `ADAMHALL_PASSWORD` als Laufzeit-Secrets gesetzt, führt ProcurementCore den offiziellen Azure-B2C-PKCE-Login ausschließlich serverseitig aus und ergänzt den kundenspezifischen Preis aus der zeitlich begrenzt zwischengespeicherten Preisliste. Blockiert STE᙭24 den direkten HTML-Abruf mit einer Cloudflare-Challenge, verifiziert ProcurementCore den Produktlink gegen die offizielle STE᙭24-Sitemap und erzeugt aus der kanonischen Produkt-URL eine als eingeschränkt gekennzeichnete Vorschau mit Artikelnummer, Hersteller und erkennbaren Variantenmerkmalen; nicht öffentlich verfügbare Werte wie der Preis bleiben leer. Alle erkannten Originalattribute werden unabhängig von einer Kategorie am Artikel gespeichert, in der Detailansicht angezeigt und von der Katalogsuche berücksichtigt; passende Kategoriefelder werden zusätzlich als typisierte Parameter geführt. JavaScript-only-Shops oder andere Seiten mit Bot-Schutz können unvollständige Daten liefern; alle erkannten Werte bleiben deshalb vor dem Import editierbar.

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

Alle fachlichen Endpunkte liegen unter `/api/v1` und erwarten das gemeinsame SSO-Cookie oder einen Bearer-Token. Wichtige Ressourcen sind `/products`, `/product-links`, `/suppliers`, `/alerts`, `/requisitions`, `/orders`, `/dashboard` und `/export/spend.csv`. Über `/products/:id/warehouse-link` werden bestehende Produkte verknüpft oder wieder getrennt. `GET /health` und `GET /api/v1/branding` sind öffentlich.

## Datenhaltung

ProcurementCore verwendet die gemeinsame PostgreSQL-Instanz. Die Tabellen werden beim Start idempotent migriert; die prüfbare SQL-Basis liegt unter `migrations/`. `core_product_links` hält ausschließlich die eindeutige Zuordnung zwischen den eigenständig gepflegten Produktstämmen – es findet keine verdeckte Überschreibung von Warehouse-Werten statt. Geldwerte werden überall als Integer-Cent gespeichert.
