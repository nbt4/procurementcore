# Feature- und Produktplan

## Recherchebasis

Die erste Version übernimmt die wiederkehrenden Kernmuster etablierter Procurement-Lösungen: Guided Buying und Katalogfilter, Bedarfsmeldungen und Freigaben, Lieferanten-Preislisten, Angebotsvergleich, Bestellungen, Wareneingang sowie Spend-Auswertung. Als Referenz dienten die offiziellen Produkt- und Hilfeseiten von SAP Ariba, Microsoft Dynamics 365 Procurement und Odoo Purchase 19.

## V1 – umgesetzt

1. Bedarf erfassen: Katalog- oder Freitextpositionen, Kostenstelle, Begründung und Bedarfsdatum.
2. Freigeben: Entwurf, Einreichung, Admin-Freigabe/Ablehnung und dokumentierte Entscheidung.
3. Sourcing: Lieferanten, Preferred-Status, Rating, Risiko, Konditionen und Lieferzeiten.
4. Katalog: Kategorien mit dynamischem Parameterschema und exakten Parameterfiltern.
5. Preise: Angebote je Lieferant, Preisverlauf, Mindestmengen, Packgrößen und Einkaufslinks.
6. Tiefpreis: persönliche Zielpreise und automatische Auslösung bei passendem Angebot.
7. Bestellen: Bedarf in Bestellung umwandeln oder Direktbestellung anlegen.
8. Empfangen: Teil- und Komplettwareneingänge mit Mengenprüfung.
9. Steuern: Spend, Einsparungen, offene Freigaben, Preisalarme, Aktivitätslog und CSV-Export.
10. Plattform: Cores-SSO, Adminrechte, Branding, Dashboard, Healthcheck, Docker und PostgreSQL.
11. Shop-Import: JSON-LD, schema.org-Microdata, OpenGraph und kontrollierte Adapter für Veranstaltungstechnik, Kabel, Stecker, Infrastruktur, Euroboxen und Casebau.

## Sinnvolle nächste Integrationen

- Automatische Bestandsbedarfe aus WarehouseCore über eine versionierte interne API
- RFQ-Versand per E-Mail und Lieferantenantworten als Dokumente
- Rechnungsprüfung (2-/3-Wege-Abgleich) mit RentalCore/Finanzexport
- CSV/XLSX-Import für große Lieferantenpreislisten
- Zeitgesteuerte Preisaktualisierungen und Shop-Jobs auf Basis der kontrollierten Adapter; für vertraglich angebundene Lieferanten PunchOut oder offizielle APIs
