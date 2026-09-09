import { useEffect, useState } from "react";
import { AlertCircle, RefreshCw, ShoppingCart } from "lucide-react";
import { api } from "../lib/api";
import type { AdamHallCart, AdamHallOrderResult, Order, Supplier } from "../lib/types";
import { Button, Field, Modal } from "./ui";

const money = (cents: number, currency: string) =>
  new Intl.NumberFormat("de-DE", { style: "currency", currency: currency || "EUR" }).format(cents / 100);

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
  const [cart, setCart] = useState<AdamHallCart | null>(null);
  const [loading, setLoading] = useState(true);
  const [submitting, setSubmitting] = useState(false);
  const [confirmed, setConfirmed] = useState(false);
  const [error, setError] = useState("");

  const load = async () => {
    setLoading(true);
    setError("");
    setConfirmed(false);
    try {
      setCart(await api<AdamHallCart>(`/orders/${order.id}/adam-hall/cart`, { method: "POST", body: "{}" }));
    } catch (caught) {
      setCart(null);
      setError((caught as Error).message);
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    void load();
  }, [order.id]);

  const placeOrder = async () => {
    setSubmitting(true);
    setError("");
    try {
      onOrdered(await api<AdamHallOrderResult>(`/orders/${order.id}/adam-hall/order`, { method: "POST", body: "{}" }));
    } catch (caught) {
      setError((caught as Error).message);
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <Modal
      title={`Adam-Hall-Warenkorb · ${order.number}`}
      wide
      onClose={onClose}
      footer={
        <>
          <Button variant="ghost" onClick={onClose} disabled={submitting}>Abbrechen</Button>
          <Button variant="primary" onClick={() => void placeOrder()} disabled={!cart || !confirmed || loading || submitting}>
            <ShoppingCart size={16} /> {submitting ? "Wird bestellt …" : "Verbindlich bestellen"}
          </Button>
        </>
      }
    >
      {loading && <div className="empty" role="status"><RefreshCw size={18} className="spin" /> Adam-Hall-Warenkorb wird geladen …</div>}
      {error && <div className="notice" role="alert"><AlertCircle size={17} /> <span>{error}</span></div>}
      {!loading && !cart && (
        <Button variant="ghost" onClick={() => void load()}><RefreshCw size={15} /> Erneut versuchen</Button>
      )}
      {cart && (
        <>
          <div className="form-grid">
            <Field label="Adam-Hall-Konto"><span>{cart.customer || "Konfiguriertes Geschäftskonto"}</span></Field>
            <Field label="Zahlungsart"><span>{cart.paymentMethod || "Kontostandard"}</span></Field>
            <Field label="Versandart"><span>{cart.shippingMethod || "Standardversand"}</span></Field>
            <Field label="Lieferadresse" full><span>{cart.shippingAddress || "Im Adam-Hall-Konto hinterlegte Standardadresse"}</span></Field>
          </div>
          <div className="table-wrap">
            <table>
              <thead><tr><th>Artikel</th><th>Menge</th><th>Einzelpreis</th><th>Summe</th></tr></thead>
              <tbody>
                {cart.lines.map((line) => (
                  <tr key={line.productNumber}>
                    <td><span className="cell-title">{line.description}</span><div className="cell-sub">{line.productNumber}</div></td>
                    <td>{line.quantity} Stk.</td>
                    <td>{money(line.unitPriceCents, cart.currency)}</td>
                    <td>{money(line.totalCents, cart.currency)}</td>
                  </tr>
                ))}
              </tbody>
              <tfoot><tr><th colSpan={3}>Gesamt laut Adam Hall</th><th>{money(cart.totalCents, cart.currency)}</th></tr></tfoot>
            </table>
          </div>
          <label className="order-confirmation">
            <input type="checkbox" checked={confirmed} onChange={(event) => setConfirmed(event.target.checked)} />
            <span>Ich habe Positionen, Gesamtpreis, Lieferadresse und Zahlungsart geprüft und bestelle verbindlich bei Adam Hall.</span>
          </label>
          <p className="cell-sub">Der Warenkorb wird bei der Bestätigung neu aufgebaut. Der dann gültige Adam-Hall-Preis wird in ProcurementCore übernommen.</p>
        </>
      )}
    </Modal>
  );
}
