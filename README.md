# ProcurementCore

ProcurementCore ist der Einkaufs-Service des Cores-Ökosystems. Er verbindet Bedarfsmeldungen, Lieferanten, parametrisierbare Artikel, Bezugsquellen, Tiefpreis-Alarme, Freigaben, Bestellungen und Wareneingänge in einem durchgängigen Prozess.

## Funktionsumfang

- Katalog mit frei definierbaren Kategorieparametern und parameterbasierter Suche
- Lieferantenstamm mit Preferred-Status, Konditionen, Lieferzeit, Bewertung und Risiko
- Mehrere Angebote pro Artikel mit Einkaufslink, Mindestmenge, Packgröße und Preisverlauf
- Tiefpreis-Alarme, die bei neuen oder geänderten Angeboten automatisch auslösen
- Bedarfsmeldungen mit Entwurf, Einreichung, Freigabe, Ablehnung und Bestellkonvertierung
- Direktbestellungen, Lieferstatus, Teil- und Komplettwareneingänge
- Spend-, Einsparungs- und Aktivitätsübersicht sowie CSV-Export
- Gemeinsames Cores-SSO über `cores_token`, zentrales Branding und responsive Cores-Oberfläche

## Entwicklung

Voraussetzungen: Go 1.25, Node.js 22 und PostgreSQL 16.

```bash
cp .env.example .env
cd web && npm ci && npm run build
cd .. && go test ./...
go run ./cmd/server
```

Der Service läuft standardmäßig auf Port `8084`. `CORES_JWT_SECRET` und die PostgreSQL-Zugangsdaten müssen denen des Cores-Stacks entsprechen.

## Container

```bash
docker build -t nobentie/procurementcore:latest .
docker run --rm -p 8084:8084 --env-file .env nobentie/procurementcore:latest
```

Im Gesamt-Stack wird ProcurementCore über `nobentie/procurementcore:latest` gestartet und im Cores-Dashboard unter `/procurement/` sowie optional über eine eigene öffentliche Domain erreichbar gemacht.

## API

Alle fachlichen Endpunkte liegen unter `/api/v1` und erwarten das gemeinsame SSO-Cookie oder einen Bearer-Token. Wichtige Ressourcen sind `/products`, `/suppliers`, `/alerts`, `/requisitions`, `/orders`, `/dashboard` und `/export/spend.csv`. `GET /health` und `GET /api/v1/branding` sind öffentlich.

## Datenhaltung

ProcurementCore verwendet die gemeinsame PostgreSQL-Instanz. Die Tabellen werden beim Start idempotent migriert; die prüfbare SQL-Basis liegt unter `migrations/`. Geldwerte werden überall als Integer-Cent gespeichert.
