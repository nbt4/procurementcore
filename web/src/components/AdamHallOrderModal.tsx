import { type CSSProperties, useEffect, useRef, useState } from "react";
import { AlertCircle, ExternalLink, RefreshCw, ShoppingCart } from "lucide-react";
import { api } from "../lib/api";
import { suiteLanguage, suiteLocale } from "../lib/cores-design";
import type { AdamHallCart, AdamHallOrderResult, Order, Supplier } from "../lib/types";
import { Button, Field, Modal } from "./ui";

const money = (cents: number, currency: string) =>
  new Intl.NumberFormat(suiteLocale(), { style: "currency", currency: currency || "EUR" }).format(cents / 100);

// Use the suite table typography/spacing. Secondary text keeps business
// confirmations readable when legacy service styles use muted light text.
const cellStyle: CSSProperties = { padding: "var(--space-3) var(--space-4)", fontSize: "var(--text-sm)" };
const headingStyle: CSSProperties = { ...cellStyle, fontSize: "var(--text-xs)", fontWeight: "var(--weight-semibold)", color: "var(--text-secondary)" };
const numberStyle: CSSProperties = { ...cellStyle, textAlign: "right" };
const numberHeadingStyle: CSSProperties = { ...headingStyle, textAlign: "right" };
const detailStyle: CSSProperties = { fontSize: "var(--text-xs)", color: "var(--text-secondary)" };

type Review = {
  preview: true;
  ready_to_execute: boolean;
  expected_updated_at: string;
  expected_context: string;
  required_confirmation_text: string;
  checkout_id?: number;
  current: Order;
  supplier_items: { productNumber: string; quantity: number }[] | null;
  supplier_cart?: AdamHallCart;
};
type CartResult = { operation_status: string; checkout_id: number; supplier_cart?: AdamHallCart };
type Attempt = { path: string; key: string; body: string };

export const ADAM_HALL_CART_URL = "https://www.adamhall.com/shop/de/checkout/cart";

export function AdamHallCartLink() {
  return (
    <a className="btn ghost" href={ADAM_HALL_CART_URL} target="_blank" rel="noopener noreferrer">
      <ExternalLink size={16} /> {suiteLanguage() === "en" ? "Open Adam Hall cart" : "Warenkorb bei Adam Hall öffnen"}
    </a>
  );
}

export function isAdamHallSupplier(supplier?: Supplier) {
  if (!supplier) return false;
  const compact = `${supplier.name}${supplier.code}`.toLowerCase().replace(/[\s_-]/g, "");
  if (compact.includes("adamhall")) return true;
  try {
    const host = new URL(supplier.website).hostname.toLowerCase();
    return host === "adamhall.com" || host.endsWith(".adamhall.com");
  } catch {
    return false;
  }
}

export default function AdamHallOrderModal({
  order,
  onClose,
  onOrdered,
}: {
  order: Order;
  onClose: () => void;
  onOrdered: (result: AdamHallOrderResult) => void;
}) {
  const [review, setReview] = useState<Review | null>(null);
  const [stage, setStage] = useState<"cart" | "send">("cart");
  const [language, setLanguage] = useState(suiteLanguage);
  const [loading, setLoading] = useState(true);
  const [submitting, setSubmitting] = useState(false);
  const [confirmed, setConfirmed] = useState(false);
  const [error, setError] = useState("");
  const [pending, setPending] = useState(false);
  const [blocked, setBlocked] = useState(false);
  const content = useRef<HTMLDivElement>(null);
  const busy = useRef(false);
  const attempt = useRef<Attempt | null>(null);
  const close = useRef(onClose);
  close.current = onClose;
  const t = (de: string, en: string) => language === "en" ? en : de;

  const load = async (operation: "cart" | "send") => {
    setLoading(true);
    setError("");
    setConfirmed(false);
    try {
      const value = await api<Review>(`/orders/${order.id}/adam-hall/${operation === "cart" ? "review" : "send-review"}`);
      setReview(value);
      setStage(operation);
      setBlocked(!value.ready_to_execute);
    } catch (caught) {
      setReview(null);
      setError((caught as Error).message);
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    let active = true;
    // Reading the local draft never logs in, rebuilds a cart or places an order.
    setLoading(true);
    setReview(null);
    setStage("cart");
    setError("");
    setPending(false);
    setBlocked(false);
    setConfirmed(false);
    attempt.current = null;
    void api<Review>(`/orders/${order.id}/adam-hall/review`).then((value) => {
      if (active) { setReview(value); setBlocked(!value.ready_to_execute); }
    }).catch((caught: Error) => { if (active) setError(caught.message); })
      .finally(() => { if (active) setLoading(false); });
    return () => { active = false; };
  }, [order.id]);

  useEffect(() => {
    const update = () => setLanguage(suiteLanguage());
    window.addEventListener("cores:languagechange", update);
    const dialog = content.current?.closest<HTMLElement>("[role=dialog]");
    const previous = document.activeElement as HTMLElement | null;
    if (dialog) { dialog.tabIndex = -1; dialog.focus(); }
    const keyboard = (event: KeyboardEvent) => {
      if (event.key === "Escape") { event.preventDefault(); if (!busy.current) close.current(); }
      if (event.key !== "Tab" || !dialog) return;
      const controls = Array.from(dialog.querySelectorAll<HTMLElement>("button:not(:disabled), a[href], input:not(:disabled), select:not(:disabled), textarea:not(:disabled)"))
        .filter((node) => node.getClientRects().length > 0);
      const first = controls[0], last = controls[controls.length - 1];
      if (!first) { event.preventDefault(); dialog.focus(); return; }
      if (!dialog.contains(document.activeElement)) { event.preventDefault(); (event.shiftKey ? last : first).focus(); return; }
      if (event.shiftKey && (document.activeElement === first || document.activeElement === dialog)) { event.preventDefault(); last.focus(); }
      else if (!event.shiftKey && (document.activeElement === last || document.activeElement === dialog)) { event.preventDefault(); first.focus(); }
    };
    document.addEventListener("keydown", keyboard);
    return () => { window.removeEventListener("cores:languagechange", update); document.removeEventListener("keydown", keyboard); previous?.focus(); };
  }, []);

  useEffect(() => {
    if (loading || submitting) return;
    const dialog = content.current?.closest<HTMLElement>("[role=dialog]");
    if (dialog && !dialog.contains(document.activeElement)) {
      (dialog.querySelector<HTMLElement>('input[type="checkbox"]:not(:disabled)') || dialog).focus();
    }
  }, [loading, submitting, stage]);

  const execute = async () => {
    if (!review || !confirmed || busy.current || pending || blocked) return;
    // Preserve both payload and key after an error or a lost response. A retry
    // inspects/finalizes that same durable action; it cannot create a new send.
    if (!attempt.current) attempt.current = {
      path: `/orders/${order.id}/adam-hall/${stage === "cart" ? "cart" : "order"}`,
      key: "adam-hall-" + Array.from(crypto.getRandomValues(new Uint8Array(16)), (value) => value.toString(16).padStart(2, "0")).join(""),
      body: JSON.stringify({ id: order.id, checkout_id: review.checkout_id || 0,
        expected_updated_at: review.expected_updated_at, expected_context: review.expected_context,
        confirmation_text: review.required_confirmation_text, confirm_change: true }),
    };
    busy.current = true;
    setSubmitting(true);
    setError("");
    try {
      const request = attempt.current;
      if (stage === "send") {
        const result = await api<AdamHallOrderResult | Review>(request.path, { method: "POST", headers: { "Idempotency-Key": request.key }, body: request.body });
        if ("order" in result && result.order && result.cart) {
          onOrdered(result);
        } else {
          setBlocked(true);
          setConfirmed(false);
          setError(t("Die geprüfte Bestellung hat sich geändert oder die Lieferanten-Vorschau ist abgelaufen. Bitte Bestellstatus und Lieferantenkonto prüfen.", "The reviewed order changed or the supplier preview expired. Check order status and the supplier account."));
        }
      } else {
        const result = await api<CartResult>(request.path, { method: "POST", headers: { "Idempotency-Key": request.key }, body: request.body });
        if (result.operation_status === "ready") {
          attempt.current = null;
          setStage("send");
          setReview(null);
          await load("send");
        } else if (result.operation_status === "pending") {
          setPending(true);
          setConfirmed(false);
        } else {
          setBlocked(true);
          setConfirmed(false);
          setError(t("Die Warenkorbvorbereitung konnte nicht abgeschlossen werden. Bitte den Warenkorb im Geschäftskonto prüfen.", "Cart preparation could not be completed. Check the cart in the business account."));
        }
      }
    } catch (caught) {
      setError((caught as Error).message);
    } finally {
      busy.current = false;
      setSubmitting(false);
    }
  };

  return (
    <Modal
      title={`${t("Adam-Hall-Bestellung", "Adam Hall order")} · ${order.number}`}
      wide
      onClose={() => { if (!busy.current) onClose(); }}
      footer={
        <>
          <Button variant="ghost" onClick={onClose} disabled={submitting}>{t("Schließen", "Close")}</Button>
          <Button variant="primary" onClick={() => void execute()} disabled={!review || !confirmed || loading || submitting || pending || blocked}>
            <ShoppingCart size={16} /> {submitting ? t("Wird verarbeitet …", "Processing …") : error && attempt.current ? t("Gespeicherten Vorgang prüfen", "Check saved action") : stage === "cart" ? t("Warenkorb vorbereiten", "Prepare cart") : t("Verbindlich bestellen", "Place paid order")}
          </Button>
        </>
      }
    >
      <div ref={content} aria-busy={loading || submitting}>
      {loading && <div className="empty" role="status"><RefreshCw size={18} className="spin" /> {t("Lokale Vorschau wird geladen …", "Loading local preview …")}</div>}
      {error && <div className="notice" role="alert"><AlertCircle size={17} /> <span>{error}</span></div>}
      {!loading && !review && !attempt.current && (
        <Button variant="ghost" onClick={() => void load(stage)}><RefreshCw size={15} /> {t("Vorschau erneut laden", "Reload preview")}</Button>
      )}
      {pending && <p role="status">{t("Der Warenkorb-Vorgang läuft oder sein Ergebnis ist noch unklar. Bitte den Lieferanten-Warenkorb prüfen. Es wurde keine kostenpflichtige Bestellung ausgelöst.", "Cart preparation is pending or its outcome is unresolved. Check the supplier cart. No paid order was requested.")}</p>}
      {review && blocked && <p role="status">{t("Diese Vorschau kann nicht bestätigt werden. Bitte Bestellstatus, Artikel und Lieferantenkonfiguration prüfen; eine ältere Lieferanten-Vorschau kann abgelaufen sein.", "This preview cannot be confirmed. Check order status, items and supplier configuration; an earlier supplier preview may have expired.")}</p>}
      {review && stage === "cart" && <>
        <p>{t("Das Öffnen dieses Dialogs ändert nichts bei Adam Hall. Die nächste Bestätigung bereitet nur den Warenkorb vor. Vorhandene fremde Positionen werden nicht entfernt.", "Opening this dialog changes nothing at Adam Hall. The next confirmation only prepares the cart. Existing unrelated items are retained.")}</p>
        <div className="suite-table-wrap"><table>
          <thead><tr><th style={headingStyle}>{t("Artikel", "Item")}</th><th style={numberHeadingStyle}>{t("Menge", "Quantity")}</th></tr></thead>
          <tbody data-suite-i18n-ignore>{(review.supplier_items || []).map((item) => <tr key={item.productNumber}><td style={cellStyle}>{item.productNumber}</td><td style={numberStyle}>{item.quantity}</td></tr>)}</tbody>
        </table></div>
        <p className="cell-sub" style={detailStyle}>{t("Lieferantenpreise, Geschäftsadressen und Zahlungsart werden anschließend zur gesonderten Bestellprüfung angezeigt.", "Supplier prices, business addresses and payment method will then be shown for a separate order review.")}</p>
      </>}
      {review?.supplier_cart?.lines?.length && stage === "send" ? (() => { const cart = review.supplier_cart; return (
        <>
          <div className="form-grid">
            <Field label={t("Adam-Hall-Konto", "Adam Hall account")}><span>{t("Konfiguriertes Geschäftskonto", "Configured business account")}</span></Field>
            <Field label={t("Zahlungsart", "Payment method")}><span data-suite-i18n-ignore>{cart.paymentMethod}</span></Field>
            <Field label={t("Versandart", "Shipping method")}><span data-suite-i18n-ignore>{cart.shippingMethod}</span></Field>
            <Field label={t("Lieferadresse", "Delivery address")} full><span data-suite-i18n-ignore>{cart.shippingAddress}</span></Field>
            <Field label={t("Rechnungsadresse", "Billing address")} full><span data-suite-i18n-ignore>{cart.billingAddress}</span></Field>
          </div>
          <div className="catalog-actions">
            <AdamHallCartLink />
          </div>
          <p className="cell-sub" style={detailStyle}>{t("Adam Hall kann im neuen Tab die Anmeldung am selben Geschäftskonto verlangen.", "Adam Hall may require signing in to the same business account in the new tab.")}</p>
          <div className="suite-table-wrap">
            <table>
              <thead><tr><th style={headingStyle}>{t("Artikel", "Item")}</th><th style={numberHeadingStyle}>{t("Menge", "Quantity")}</th><th style={numberHeadingStyle}>{t("Einzelpreis", "Unit price")}</th><th style={numberHeadingStyle}>{t("Summe", "Total")}</th></tr></thead>
              <tbody data-suite-i18n-ignore>
                {cart.lines.map((line) => (
                  <tr key={line.productNumber}>
                    <td style={cellStyle}><span className="cell-title">{line.description}</span><div className="cell-sub" style={detailStyle}>{line.productNumber}</div></td>
                    <td style={numberStyle}>{line.quantity}</td>
                    <td style={numberStyle}>{money(line.unitPriceCents, cart.currency)}</td>
                    <td style={numberStyle}>{money(line.totalCents, cart.currency)}</td>
                  </tr>
                ))}
              </tbody>
              <tfoot><tr><th colSpan={3} style={headingStyle}>{t("Gesamt laut Adam Hall", "Adam Hall total")}</th><th style={numberHeadingStyle}>{money(cart.totalCents, cart.currency)}</th></tr></tfoot>
            </table>
          </div>
          <p className="cell-sub" style={detailStyle}>{t("Die Bestellung verwendet genau diesen geprüften Warenkorb. Geänderte Preise, Positionen, Adressen oder Zahlungs- und Versandarten stoppen die Übermittlung. Der angezeigte Preis wird in ProcurementCore übernommen.", "The order uses this exact reviewed cart. Changed prices, items, addresses or payment/shipping methods stop submission. ProcurementCore adopts the displayed price.")}</p>
        </>
      ); })() : null}
      {review && !pending && !blocked && <label className="order-confirmation">
        <input type="checkbox" checked={confirmed} disabled={submitting || loading} onChange={(event) => setConfirmed(event.target.checked)} />
        <span>{stage === "cart" ? t("Ich habe Artikel und Mengen geprüft und bestätige die Änderung des Lieferanten-Warenkorbs. Dies löst noch keine kostenpflichtige Bestellung aus.", "I reviewed the items and quantities and confirm changing the supplier cart. This does not place a paid order.") : t("Ich habe Positionen, Gesamtpreis, Liefer- und Rechnungsadresse sowie Zahlungs- und Versandart geprüft und bestelle verbindlich bei Adam Hall.", "I reviewed items, total price, delivery and billing addresses and payment/shipping methods and confirm this paid Adam Hall order.")}</span>
      </label>}
      {error && attempt.current && <p className="cell-sub" style={detailStyle}>{t("Eine Wiederholung prüft denselben gespeicherten Vorgang. Bei unklarem Bestellstatus zuerst das Lieferantenkonto prüfen und keine Ersatzbestellung erstellen.", "A retry checks the same saved action. If order status is unresolved, check the supplier account before creating any replacement order.")}</p>}
      </div>
    </Modal>
  );
}
