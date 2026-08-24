# ProcurementCore

ProcurementCore ist der Einkaufs-Service des Cores-Ökosystems. Er verbindet Bedarfsmeldungen, Lieferanten, parametrisierbare Artikel, Bezugsquellen, Tiefpreis-Alarme, Freigaben, Bestellungen und Wareneingänge in einem durchgängigen Prozess.

## Funktionsumfang

- Sortierbarer tabellarischer Artikelkatalog mit bearbeitbaren Kategorien, visuellem Parameter-Editor und parameterbasierter Suche ohne JSON-Eingabe
- Sicheres Löschen ungenutzter Kategorien; verwendete Kategorien bleiben gegen Datenverlust geschützt
- Artikelimport aus Produktlinks mit prüfbarer Vorschau, direkt editierbaren Kategorieparametern und automatischer Vorbelegung aus Schema.org/JSON-LD, OpenGraph sowie Adam-Hall-Shop-Produktdetails
- Lieferantenstamm mit Preferred-Status, Konditionen, Lieferzeit, Bewertung und Risiko
- Mehrere Angebote pro Artikel mit Einkaufslink, Mindestmenge, Packgröße und Preisverlauf
- Tiefpreis-Alarme, die bei neuen oder geänderten Angeboten automatisch auslösen
- Bedarfsmeldungen mit Entwurf, Einreichung, Freigabe, Ablehnung und Bestellkonvertierung
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

Der Service läuft standardmäßig auf Port `8084`. `CORES_JWT_SECRET` und die PostgreSQL-Zugangsdaten müssen denen des Cores-Stacks entsprechen.

Der Linkimport ruft ausschließlich öffentliche HTTP(S)-Ziele auf Standardports ab, begrenzt Laufzeit und Weiterleitungen und blockiert interne, lokale sowie Link-Local-Netze. Von sehr großen Shopseiten werden höchstens die ersten 16 MB verarbeitet; liegen die Produktdaten wie üblich früh im Dokument, funktioniert der Import auch bei insgesamt größeren Seiten. Adam-Hall-Shopseiten werden zusätzlich anhand ihrer serverseitig gerenderten Artikeldaten erkannt, sodass Artikelnummer, Marke und technische Spezifikationen übernommen werden. Sind `ADAMHALL_USERNAME` und `ADAMHALL_PASSWORD` als Laufzeit-Secrets gesetzt, führt ProcurementCore den offiziellen Azure-B2C-PKCE-Login ausschließlich serverseitig aus und ergänzt den kundenspezifischen Preis aus der zeitlich begrenzt zwischengespeicherten Preisliste. Blockiert STE᙭24 den direkten HTML-Abruf mit einer Cloudflare-Challenge, verifiziert ProcurementCore den Produktlink gegen die offizielle STE᙭24-Sitemap und erzeugt aus der kanonischen Produkt-URL eine als eingeschränkt gekennzeichnete Vorschau mit Artikelnummer, Hersteller und erkennbaren Variantenmerkmalen; nicht öffentlich verfügbare Werte wie der Preis bleiben leer. Alle erkannten Originalattribute werden unabhängig von einer Kategorie am Artikel gespeichert, in der Detailansicht angezeigt und von der Katalogsuche berücksichtigt; passende Kategoriefelder werden zusätzlich als typisierte Parameter geführt. JavaScript-only-Shops oder andere Seiten mit Bot-Schutz können unvollständige Daten liefern; alle erkannten Werte bleiben deshalb vor dem Import editierbar.

## Container

```bash
docker build -t nobentie/procurementcore:latest .
docker run --rm -p 8084:8084 --env-file .env nobentie/procurementcore:latest
```

Im Gesamt-Stack läuft ProcurementCore als eigenständiger Compose-Service auf Host-Port `8084`. Die öffentliche URL ist `https://procurement.tsunami-events.de`; das Cores-Dashboard verlinkt dorthin und stellt ausdrücklich keinen `/procurement`-Proxy bereit.

## API

Alle fachlichen Endpunkte liegen unter `/api/v1` und erwarten das gemeinsame SSO-Cookie oder einen Bearer-Token. Wichtige Ressourcen sind `/products`, `/suppliers`, `/alerts`, `/requisitions`, `/orders`, `/dashboard` und `/export/spend.csv`. `GET /health` und `GET /api/v1/branding` sind öffentlich.

## Datenhaltung

ProcurementCore verwendet die gemeinsame PostgreSQL-Instanz. Die Tabellen werden beim Start idempotent migriert; die prüfbare SQL-Basis liegt unter `migrations/`. Geldwerte werden überall als Integer-Cent gespeichert.
