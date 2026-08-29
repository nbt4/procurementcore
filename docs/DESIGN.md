# ProcurementCore im Cores Designsystem

Die vollständigen und verbindlichen UI-Regeln liegen im Umbrella-Repository unter [`docs/DESIGN_SYSTEM.md`](https://github.com/nbt4/cores/blob/main/docs/DESIGN_SYSTEM.md). Dieses Dokument beschreibt ausschließlich die fachliche Ausprägung von ProcurementCore.

## Fachliche Dashboard-Struktur

ProcurementCore verwendet den gemeinsamen Dashboard-Vertrag in dieser Ausprägung:

1. zeitabhängige persönliche Begrüßung und Beschreibung der Einkaufslage;
2. vier Kennzahlen: offene Freigaben, ausgelöste Tiefpreise, Bestellvolumen und realisierte Einsparung;
3. „Jetzt bearbeiten“ für Freigaben und Preisalarme sowie ein Schnellstart für Bedarf, Katalog, Lieferanten und Bestellungen;
4. der fünfstufige Beschaffungsablauf;
5. letzte Einkaufsaktivitäten.

## Zulässige fachliche Besonderheiten

- Geldwerte und technische Referenzen dürfen JetBrains Mono verwenden.
- Erfolg, Warnung, Fehler und Information verwenden ausschließlich die suite-weiten semantischen Farben.
- Katalogparameter und Einkaufsstatus dürfen kompakt dargestellt werden, verändern aber weder Typografie-Leiter noch Tabellen- oder Formularstruktur.
- Die frühere Petrol-/Aptos-Palette und besonders kleinen 2–9-px-Radien sind außer Kraft.

## Implementierung

`web/src/cores-theme.css` und `web/src/lib/cores-design.ts` sind generierte Dateien. Änderungen erfolgen in den kanonischen Quellen des Umbrella-Repositories und werden dort synchronisiert und geprüft. Lokale Komponenten verwenden die `suite-*`-Primitives und dürfen sie nur fachlich ergänzen.
