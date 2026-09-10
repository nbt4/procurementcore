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

## Wareneingang

Der Wareneingangsdialog zeigt vor der Buchung die Warehouse-Zuordnung und die
Auswirkung der Eingangsmenge. Bei Mengenführung werden alter und neuer Bestand,
bei Einzelverfolgung die Zahl der neu entstehenden und anschließend vorhandenen
Devices genannt. Fehlt die Zuordnung, bleibt die Buchungsaktion deaktiviert;
der Dialog bietet erkannte Warehouse-Produkte zum Verknüpfen sowie die
vorausgefüllte Neuanlage an. Lade-, Fehler- und Leerezustand bleiben im Dialog
sichtbar und die Verknüpfung kann nach einer externen Neuanlage erneut geprüft
werden. Auf schmalen Viewports stehen Auswahl und Aktionen untereinander.

## Adam-Hall-Warenkorb

Nach der Umwandlung eines freigegebenen Bedarfs bei einem Adam-Hall-Lieferanten
öffnet ProcurementCore den Live-Warenkorb automatisch. Bei bestehenden
Bestellungsentwürfen ist dieselbe Aktion im Bestellungsdialog erreichbar. Der
Dialog hält während der Serveranfrage seine Struktur, zeigt Fehler mit einer
Wiederholungsaktion und nennt Geschäftskonto, Lieferadresse, Versand- und
Zahlungsart sowie jede Position und den aktuellen Gesamtpreis. Die verbindliche
Bestellaktion bleibt bis zur ausdrücklichen Kontrollbestätigung deaktiviert.
Zugangsdaten und Shopware-Kontext werden nie an den Browser ausgegeben.

## PDF-Bestellimport

Die Bestellungsseite bietet neben der roten Primäraktion für Direktbestellungen
eine sekundäre PDF-Importaktion. Der erste Dialog enthält Dateiauswahl,
Größen-/Seitenlimit sowie einen stabilen Lade- und Fehlerzustand. Nach der
Analyse folgt das bestehende breite Bestellformular mit allen editierbaren
Feldern. Erkennungsgrad, Quelldatei, Summenabweichung und einzelne Prüfhinweise
stehen vor den Formularfeldern und verwenden ausschließlich die semantischen
Suite-Zustände. Die Anlage bleibt eine ausdrückliche Primäraktion; ein Upload
allein erzeugt noch keine Bestellung. Mobile Formulare und Positionszeilen
brechen gemäß den bestehenden Regeln einspaltig beziehungsweise zweispaltig um.

## Implementierung

`web/src/cores-theme.css` und `web/src/lib/cores-design.ts` sind generierte Dateien. Änderungen erfolgen in den kanonischen Quellen des Umbrella-Repositories und werden dort synchronisiert und geprüft. Lokale Komponenten verwenden die `suite-*`-Primitives und dürfen sie nur fachlich ergänzen.
