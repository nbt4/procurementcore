# ProcurementCore

ProcurementCore ist der Einkaufs-Service des Cores-Ökosystems. Er verbindet Bedarfsmeldungen, Lieferanten, parametrisierbare Artikel, Bezugsquellen, Tiefpreis-Alarme, Freigaben, Bestellungen und Wareneingänge in einem durchgängigen Prozess.

## Funktionsumfang

- Katalog mit bearbeitbaren Kategorien, visuellem Parameter-Editor und parameterbasierter Suche ohne JSON-Eingabe
- Sicheres Löschen ungenutzter Kategorien; verwendete Kategorien bleiben gegen Datenverlust geschützt
- Artikelimport aus Produktlinks mit prüfbarer Vorschau, direkt editierbaren Kategorieparametern und automatischer Vorbelegung aus Schema.org/JSON-LD, OpenGraph sowie Adam-Hall-Shop-Produktdetails
- Lieferantenstamm mit Preferred-Status, Konditionen, Lieferzeit, Bewertung und Risiko
- Mehrere Angebote pro Artikel mit Einkaufslink, Mindestmenge, Packgröße und Preisverlauf
- Tiefpreis-Alarme, die bei neuen oder geänderten Angeboten automatisch auslösen
- Bedarfsmeldungen mit Entwurf, Einreichung, Freigabe, Ablehnung und Bestellkonvertierung
- Direktbestellungen, Lieferstatus, Teil- und Komplettwareneingänge
- Spend-, Einsparungs- und Aktivitätsübersicht sowie CSV-Export
- Gemeinsames Cores-SSO über `cores_token` mit zentralem Login und serviceübergreifendem Logout
- Zentrales Branding und responsive Cores-Oberfläche für Desktop und Mobilgeräte

## Entwicklung

Voraussetzungen: Go 1.25, Node.js 22 und PostgreSQL 16.

```bash
cp .env.example .env
cd web && npm ci && npm run build
cd .. && go test ./...
go run ./cmd/server
```

Der Service läuft standardmäßig auf Port `8084`. `CORES_JWT_SECRET` und die PostgreSQL-Zugangsdaten müssen denen des Cores-Stacks entsprechen.

Der Linkimport ruft ausschließlich öffentliche HTTP(S)-Ziele auf Standardports ab, begrenzt Laufzeit und Weiterleitungen und blockiert interne, lokale sowie Link-Local-Netze. Von sehr großen Shopseiten werden höchstens die ersten 16 MB verarbeitet; liegen die Produktdaten wie üblich früh im Dokument, funktioniert der Import auch bei insgesamt größeren Seiten. Adam-Hall-Shopseiten werden zusätzlich anhand ihrer serverseitig gerenderten Artikeldaten erkannt, sodass Artikelnummer, Marke und technische Spezifikationen übernommen werden. JavaScript-only-Shops oder Seiten mit Bot-Schutz können unvollständige Daten liefern; alle erkannten Werte bleiben deshalb vor dem Import editierbar.

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
