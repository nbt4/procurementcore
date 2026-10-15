# Procurement-Finanzrechte für Schreibabläufe

MCP 1.5.65 / ProcurementCore 1.0.77, 2026-10-15.

Alle 21 preisführenden Procurement-Workflows verlangen zusätzlich zum bisherigen
Aktionsrecht ausdrücklich `cores:procurement:financial`. Das gilt vor Vorschau,
Dry-run, Ausführung und jeder Wiederholung einer gespeicherten Antwort:

- Angebote: create, update, archive, restore.
- Bedarfe: create, update, submit, decide, order, archive, restore.
- Bestellungen: create, update, transition, receive, archive, restore,
  build_adam_hall_cart, send_adam_hall, send_amazon, reconcile_submission.

MCP veröffentlicht für diese Werkzeuge und ihre `prepare_*`-Partner die
zusätzliche OAuth-Anforderung im Descriptor und als konkrete Consent-Challenge.
Legacy `cores:write`, Administratorrechte und Finanzrechte anderer Dienste
ersetzen die gesonderte Einkaufsfreigabe nicht. Finanzrechte allein erlauben
keine Aktion. Bestehende Tokens erhalten keine zusätzlichen Rechte.

Produktanlage bleibt ohne Geldbezug möglich. `product_url`, `supplier_id` oder
`price_cents` erfordern die Finanzfreigabe bereits vor Shopanalyse/SQL: eine URL
kann einen Preis liefern, selbst wenn kein Preis eingegeben wurde. Der Owner
schützt `initialOffer` auch bei Nullpreis vor Transaktion und Replay. Produkt und
optionales Angebot werden weiterhin in derselben vorhandenen Transaktion angelegt.
Reine Stammdatenänderungen, Kategorien, Lieferanten und Produktverknüpfungen
benötigen dadurch keine zusätzliche Finanzfreigabe.

MCP signiert den getrennten booleschen Claim `mcp_procurement_financial` zusammen
mit dem tatsächlichen Nutzer und dem konkreten Aktionsscope. ProcurementCore
prüft Signatur, Ablauf und Nutzerbindung sowie weiterhin aktuelle fachliche
Rechte, Version, Kontext und Bestätigungsphrase. Die Prüfung erfolgt vor jedem
Owner-Beleg, einschließlich alter nativer Idempotenzbelege. Alte Beleg-Payloads,
Fingerprints und Erfolgsantworten werden unverändert beibehalten.

Auch direkte native Preis-/Bestell-/Bedarfsrouten erkennen delegierte Tokens
bei fehlendem Origin-Header und bei leerer Cookie mit Bearer-Fallback. Ein
falsch typisierter Finanzclaim wird als fehlende Freigabe behandelt, ohne die
Delegationserkennung zu verlieren. Ungestützte native Preisalarm-, URL-Bedarfs-,
PDF-Import- und Punchout-Start-Schreibwege sind für MCP gesperrt; dafür existiert
kein benannter, geprüfter MCP-Vertrag. Normale native UI-Sitzungen behalten ihre
bestehenden fachlichen Rechte.

Es gibt keine Schema-, Umgebungs- oder Tool-Schemaänderung. Der Katalog bleibt
bei 446 Werkzeugen. Clients laden Werkzeug-Metadaten neu, erteilen bei Bedarf
die zusätzliche Finanzfreigabe und erstellen danach eine frische Vorschau.

Rollout: zuerst ProcurementCore, dann MCP. Während dieses kurzen Übergangs
verweigert der neue Owner alte Delegationen ohne Finanzclaim sicher. Erst MCP
1.5.65 übermittelt die getrennte Finanzfreigabe. Keine produktiven Geschäftsdaten
werden zur Verifikation angelegt oder abgerufen. Die übergreifenden Issues
[#4](https://github.com/nbt4/cores-mcp/issues/4) und
[#5](https://github.com/nbt4/cores-mcp/issues/5) bleiben für die übrigen
Rental-/Planner-/Dokument-/Undo- und Legacy-Writer-Abnahmen `in_progress`.
