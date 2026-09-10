import { FormEvent, useEffect, useState } from "react";
import {
  ExternalLink,
  FileUp,
  Link2,
  PackageCheck,
  Plus,
  RefreshCw,
  Send,
  ShoppingCart,
  Truck,
  X,
} from "lucide-react";
import { api, date, euro } from "../lib/api";
import { warehouseProductsURL } from "../lib/app-paths";
import type {
  Order,
  OrderImportPreview,
  OrderLine,
  Product,
  Supplier,
  WarehouseProductCandidate,
} from "../lib/types";
import { Badge, Button, Empty, Field, Modal } from "../components/ui";
import { useApp } from "../App";
import AdamHallOrderModal, { isAdamHallSupplier } from "../components/AdamHallOrderModal";

const statusLabel: Record<string, string> = {
  draft: "Entwurf",
  sent: "Gesendet",
  confirmed: "Bestätigt",
  partially_received: "Teileingang",
  received: "Empfangen",
  cancelled: "Storniert",
  submitting: "Wird übertragen",
  submission_unknown: "Übertragung prüfen",
};
const tone = (status: string) =>
  status === "received"
    ? "green"
    : status === "partially_received" || status === "confirmed"
      ? "blue"
      : status === "sent"
        ? "amber"
        : status === "cancelled" || status === "submission_unknown"
          ? "red"
          : "";

const trackingLabel = (mode?: string) =>
  mode === "quantity"
    ? "Mengenbestand"
    : mode === "individual"
      ? "Einzelverfolgung"
      : "Keine Bestandsverfolgung";

export function receiptInventoryMessage(product: Product, quantity: number) {
  if (product.warehouseTrackingMode === "quantity") {
    const current = product.warehouseStockQuantity || 0;
    return `Der Warehouse-Mengenbestand steigt von ${current} auf ${current + quantity}.`;
  }
  if (product.warehouseTrackingMode === "individual") {
    const current = product.warehouseDeviceCount || 0;
    return `${quantity} neue Devices werden ohne Lagerplatz angelegt; danach sind ${current + quantity} Devices vorhanden.`;
  }
  return "Das Warehouse-Produkt führt keinen Bestand; der Eingang wird nur in ProcurementCore dokumentiert.";
}

export default function OrdersPage() {
  const { user, refreshKey, refresh, notify } = useApp();
  const [rows, setRows] = useState<Order[]>([]);
  const [suppliers, setSuppliers] = useState<Supplier[]>([]);
  const [products, setProducts] = useState<Product[]>([]);
  const [selected, setSelected] = useState<Order | null>(null);
  const [create, setCreate] = useState<"manual" | OrderImportPreview | null>(null);
  const [pdfImport, setPDFImport] = useState(false);
  const [adamHallOrder, setAdamHallOrder] = useState<Order | null>(null);
  const [receipt, setReceipt] = useState<{
    order: Order;
    line: OrderLine;
  } | null>(null);

  useEffect(() => {
    Promise.all([
      api<Order[]>("/orders"),
      api<Supplier[]>("/suppliers?active=true"),
      api<Product[]>("/products"),
    ]).then(([orders, supplierRows, productRows]) => {
      setRows(orders);
      setSuppliers(supplierRows);
      setProducts(productRows);
    });
  }, [refreshKey]);

  const update = async (row: Order, status: string) => {
    await api(`/orders/${row.id}`, {
      method: "PUT",
      body: JSON.stringify({
        status,
        supplierOrderNumber: row.supplierOrderNumber || "",
        expectedDelivery: row.expectedDelivery || null,
        notes: row.notes || "",
      }),
    });
    notify(`Status: ${statusLabel[status]}`);
    setSelected(null);
    refresh();
  };
  const open = async (id: number) =>
    setSelected(await api<Order>(`/orders/${id}`));

  return (
    <div className="content">
      <div className="page-header">
        <div>
          <h2>Bestellungen</h2>
          <p>
            Bestellung auslösen, Liefertermine verfolgen und Wareneingänge
            verbuchen.
          </p>
        </div>
        {user.isAdmin && (
          <div className="catalog-actions">
            <Button variant="ghost" onClick={() => setPDFImport(true)}>
              <FileUp size={17} /> Bestellung aus PDF
            </Button>
            <Button variant="primary" onClick={() => setCreate("manual")}>
              <Plus size={17} /> Direktbestellung
            </Button>
          </div>
        )}
      </div>
      {rows.length ? (
        <div className="table-wrap">
          <table>
            <thead>
              <tr>
                <th>Nummer</th>
                <th>Lieferant</th>
                <th>Bestellt von</th>
                <th>Liefertermin</th>
                <th>Volumen</th>
                <th>Status</th>
                <th></th>
              </tr>
            </thead>
            <tbody>
              {rows.map((row) => (
                <tr key={row.id}>
                  <td>
                    <span className="cell-title">{row.number}</span>
                    {row.supplierOrderNumber && <div className="cell-sub">Lieferant: {row.supplierOrderNumber}</div>}
                    <div className="cell-sub">{date(row.createdAt)}</div>
                  </td>
                  <td>{row.supplier?.name}</td>
                  <td>{row.orderedByName}</td>
                  <td>{date(row.expectedDelivery)}</td>
                  <td>{euro(row.totalCents)}</td>
                  <td>
                    <Badge tone={tone(row.status)}>
                      {statusLabel[row.status] || row.status}
                    </Badge>
                  </td>
                  <td>
                    <Button variant="ghost" onClick={() => void open(row.id)}>
                      Öffnen
                    </Button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      ) : (
        <Empty>Noch keine Bestellungen vorhanden.</Empty>
      )}
      {selected && (
        <Modal
          title={`${selected.number} · ${selected.supplier?.name}`}
          wide
          onClose={() => setSelected(null)}
          footer={
            <>
              <Button variant="ghost" onClick={() => setSelected(null)}>
                Schließen
              </Button>
              {user.isAdmin && selected.status === "draft" && isAdamHallSupplier(selected.supplier) && (
                <Button variant="primary" onClick={() => setAdamHallOrder(selected)}>
                  <ShoppingCart size={16} /> Adam-Hall-Warenkorb
                </Button>
              )}
              {user.isAdmin && selected.status === "draft" && (
                <Button
                  variant={isAdamHallSupplier(selected.supplier) ? "ghost" : "primary"}
                  onClick={() => void update(selected, "sent")}
                >
                  <Send size={16} /> Als gesendet markieren
                </Button>
              )}
              {user.isAdmin && selected.status === "sent" && (
                <Button
                  variant="primary"
                  onClick={() => void update(selected, "confirmed")}
                >
                  <Truck size={16} /> Bestätigt
                </Button>
              )}
              {user.isAdmin && selected.status === "submission_unknown" && (
                <>
                  <a className="btn ghost" href="https://www.adamhall.com/shop/de/account/order" target="_blank" rel="noreferrer">
                    <ExternalLink size={16} /> Adam Hall prüfen
                  </a>
                  <Button
                    variant="danger"
                    onClick={() => {
                      if (window.confirm("Nur zurücksetzen, wenn im Adam-Hall-Konto keine Bestellung angelegt wurde."))
                        void update(selected, "draft");
                    }}
                  >
                    Als Entwurf zurücksetzen
                  </Button>
                </>
              )}
              {user.isAdmin &&
                !["received", "cancelled", "submission_unknown"].includes(selected.status) && (
                  <Button
                    variant="danger"
                    onClick={() => void update(selected, "cancelled")}
                  >
                    Stornieren
                  </Button>
                )}
            </>
          }
        >
          <div className="form-grid">
            <Field label="Status">
              <Badge tone={tone(selected.status)}>
                {statusLabel[selected.status]}
              </Badge>
            </Field>
            <Field label="Erwartete Lieferung">
              <span>{date(selected.expectedDelivery)}</span>
            </Field>
            <Field label="Lieferanten-Bestellnummer" full>
              <SupplierOrderNumberEditor
                order={selected}
                disabled={!user.isAdmin}
                onSaved={async () => {
                  await open(selected.id);
                  refresh();
                  notify("Bestellnummer gespeichert");
                }}
              />
            </Field>
            <Field label="Notizen" full>
              <p>{selected.notes || "–"}</p>
            </Field>
          </div>
          <div className="table-wrap">
            <table>
              <thead>
                <tr>
                  <th>Position</th>
                  <th>Bestellt</th>
                  <th>Empfangen</th>
                  <th>Preis</th>
                  <th></th>
                </tr>
              </thead>
              <tbody>
                {selected.lines.map((line) => (
                  <tr key={line.id}>
                    <td>
                      <span className="cell-title">{line.description}</span>
                      <div className="cell-sub">{line.product?.sku}</div>
                    </td>
                    <td>
                      {line.quantity} {line.unit}
                    </td>
                    <td>
                      {line.receivedQuantity} {line.unit}
                    </td>
                    <td>{euro(line.unitPriceCents)}</td>
                    <td>
                      <div className="row-actions">
                        {line.purchaseUrl && (
                          <a
                            className="btn ghost icon"
                            href={line.purchaseUrl}
                            target="_blank"
                            rel="noreferrer"
                          >
                            <ExternalLink size={15} />
                          </a>
                        )}
                        {user.isAdmin &&
                          line.receivedQuantity < line.quantity &&
                          !["draft", "cancelled"].includes(selected.status) && (
                            <Button
                              variant="primary"
                              onClick={() => setReceipt({ order: selected, line })}
                            >
                              <PackageCheck size={15} /> Eingang
                            </Button>
                          )}
                      </div>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </Modal>
      )}
      {pdfImport && (
        <PDFOrderImportModal
          onClose={() => setPDFImport(false)}
          onPreview={(preview) => {
            setPDFImport(false);
            setCreate(preview);
          }}
        />
      )}
      {create && (
        <OrderModal
          suppliers={suppliers}
          products={products}
          initial={create === "manual" ? undefined : create}
          onClose={() => setCreate(null)}
          onSaved={() => {
            const imported = create !== "manual";
            setCreate(null);
            notify(imported ? "PDF-Bestellung angelegt" : "Bestellung angelegt");
            refresh();
          }}
        />
      )}
      {receipt && (
        <ReceiptModal
          key={`${receipt.order.id}-${receipt.line.id}-${receipt.line.receivedQuantity}`}
          value={receipt}
          onClose={() => setReceipt(null)}
          onSaved={async () => {
            const updated = await api<Order>(`/orders/${receipt.order.id}`);
            setSelected(updated);
            const currentLine = updated.lines.find((line) => line.id === receipt.line.id);
            const nextLine = currentLine && currentLine.receivedQuantity < currentLine.quantity
              ? currentLine
              : updated.lines.find((line) => line.receivedQuantity < line.quantity);
            setReceipt(nextLine ? { order: updated, line: nextLine } : null);
            notify("Wareneingang verbucht");
            refresh();
          }}
        />
      )}
      {adamHallOrder && (
        <AdamHallOrderModal
          order={adamHallOrder}
          onClose={() => setAdamHallOrder(null)}
          onOrdered={(result) => {
            setAdamHallOrder(null);
            setSelected(result.order);
            notify(`Bei Adam Hall bestellt: ${result.order.supplierOrderNumber}`);
            refresh();
          }}
        />
      )}
    </div>
  );
}

export function PDFOrderImportModal({
  onClose,
  onPreview,
}: {
  onClose: () => void;
  onPreview: (preview: OrderImportPreview) => void;
}) {
  const [file, setFile] = useState<File | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");

  const analyze = async (event: FormEvent) => {
    event.preventDefault();
    if (!file) return;
    if (file.size > 12 * 1024 * 1024) {
      setError("PDF darf maximal 12 MB groß sein.");
      return;
    }
    setLoading(true);
    setError("");
    try {
      const form = new FormData();
      form.append("file", file);
      onPreview(await api<OrderImportPreview>("/orders/import-preview", { method: "POST", body: form }));
    } catch (caught) {
      setError((caught as Error).message);
    } finally {
      setLoading(false);
    }
  };

  return (
    <Modal
      title="Bestellung aus PDF"
      onClose={onClose}
      footer={
        <>
          <Button variant="ghost" onClick={onClose} disabled={loading}>Abbrechen</Button>
          <Button variant="primary" type="submit" form="pdf-order-import-form" disabled={!file || loading}>
            <RefreshCw className={loading ? "spin" : ""} size={16} /> {loading ? "PDF wird analysiert …" : "PDF analysieren"}
          </Button>
        </>
      }
    >
      <form id="pdf-order-import-form" onSubmit={analyze}>
        {error && <div className="notice" role="alert">{error}</div>}
        <Field label="Bestell-PDF">
          <input
            type="file"
            accept="application/pdf,.pdf"
            required
            onChange={(event) => setFile(event.target.files?.[0] || null)}
          />
        </Field>
        <p className="import-hint">
          ProcurementCore liest die Textebene lokal im Service aus und schlägt Lieferant,
          Bestellnummer, Termine sowie Positionen vor. Die PDF wird nicht dauerhaft gespeichert.
          Maximal 12 MB und 100 Seiten.
        </p>
        {loading && <div className="empty" role="status">PDF-Inhalt und Katalogzuordnungen werden geprüft …</div>}
      </form>
    </Modal>
  );
}

export function OrderModal({
  suppliers,
  products,
  initial,
  onClose,
  onSaved,
}: {
  suppliers: Supplier[];
  products: Product[];
  initial?: OrderImportPreview;
  onClose: () => void;
  onSaved: () => void;
}) {
  const imported = Boolean(initial);
  const [supplierId, setSupplierId] = useState(initial?.supplierId || 0);
  const [expected, setExpected] = useState(initial?.expectedDelivery?.slice(0, 10) || "");
  const [orderDate, setOrderDate] = useState(initial?.orderDate?.slice(0, 10) || "");
  const [status, setStatus] = useState(imported ? "sent" : "draft");
  const [currency, setCurrency] = useState(initial?.currency || "EUR");
  const [supplierOrderNumber, setSupplierOrderNumber] = useState(initial?.supplierOrderNumber || "");
  const [notes, setNotes] = useState(initial ? `Nachträglich aus PDF importiert: ${initial.sourceFileName}` : "");
  const [lines, setLines] = useState<OrderLine[]>(initial?.lines.length
    ? initial.lines.map((line) => ({ ...line, receivedQuantity: 0 }))
    : [{
        description: "",
        quantity: 1,
        receivedQuantity: 0,
        unit: "Stk.",
        unitPriceCents: 0,
        purchaseUrl: "",
      }]);
  const [error, setError] = useState("");
  const [saving, setSaving] = useState(false);

  const chooseProduct = (index: number, id: number) => {
    const product = products.find((item) => item.id === id);
    const offer = product?.offers
      ?.filter((item) => !supplierId || item.supplierId === supplierId)
      .sort((left, right) => left.priceCents - right.priceCents)[0];
    setLines((current) =>
      current.map((line, lineIndex) =>
        lineIndex === index
          ? {
              ...line,
              productId: product?.id,
              description: product?.name || "",
              unit: product?.unit || "Stk.",
              unitPriceCents: offer?.priceCents || 0,
              purchaseUrl: offer?.purchaseUrl || "",
            }
          : line,
      ),
    );
  };
  const submit = async (event: FormEvent) => {
    event.preventDefault();
    setSaving(true);
    setError("");
    try {
      await api("/orders", {
        method: "POST",
        body: JSON.stringify({
          supplierId,
          status,
          currency,
          supplierOrderNumber,
          orderDate: orderDate
            ? new Date(`${orderDate}T12:00:00`).toISOString()
            : null,
          expectedDelivery: expected
            ? new Date(`${expected}T12:00:00`).toISOString()
            : null,
          notes,
          lines,
        }),
      });
      await onSaved();
    } catch (caught) {
      setError((caught as Error).message);
    } finally {
      setSaving(false);
    }
  };

  return (
    <Modal
      title={imported ? "PDF-Bestellung prüfen" : "Direktbestellung"}
      wide
      onClose={onClose}
      footer={
        <>
          <Button variant="ghost" onClick={onClose} disabled={saving}>
            Abbrechen
          </Button>
          <Button variant="primary" type="submit" form="order-form" disabled={saving}>
            {saving ? "Bestellung wird angelegt …" : imported ? "Importierte Bestellung anlegen" : "Bestellung anlegen"}
          </Button>
        </>
      }
    >
      <form id="order-form" onSubmit={submit}>
        {error && <div className="notice" role="alert">{error}</div>}
        {initial && (
          <div className="receipt-inventory">
            <div className="receipt-inventory-heading">
              <div>
                <strong>PDF-Analyse: {initial.confidence}% Erkennungsgrad</strong>
                <span>{initial.sourceFileName} · {initial.pageCount} Seite(n) · {initial.extractedCharacters} Zeichen</span>
              </div>
              <Badge tone={initial.warnings.length ? "amber" : "green"}>
                {initial.warnings.length ? `${initial.warnings.length} Prüfhinweis(e)` : "Vollständig erkannt"}
              </Badge>
            </div>
            {initial.documentTotalCents > 0 && (
              <p>
                Dokument: {new Intl.NumberFormat("de-DE", { style: "currency", currency: initial.currency }).format(initial.documentTotalCents / 100)} ·
                Positionen: {new Intl.NumberFormat("de-DE", { style: "currency", currency: initial.currency }).format(initial.recognizedTotalCents / 100)}
              </p>
            )}
            {initial.warnings.map((warning) => <span key={warning}>• {warning}</span>)}
          </div>
        )}
        <div className="form-grid">
          <Field label="Lieferant">
            <select
              required
              value={supplierId}
              onChange={(event) => setSupplierId(Number(event.target.value))}
            >
              <option value="0">Bitte wählen</option>
              {suppliers.map((supplier) => (
                <option value={supplier.id} key={supplier.id}>
                  {supplier.preferred ? "★ " : ""}
                  {supplier.name}
                </option>
              ))}
            </select>
          </Field>
          {imported && (
            <Field label="Bestelldatum">
              <input
                type="date"
                required
                value={orderDate}
                onChange={(event) => setOrderDate(event.target.value)}
              />
            </Field>
          )}
          <Field label="Erwartete Lieferung">
            <input
              type="date"
              value={expected}
              onChange={(event) => setExpected(event.target.value)}
            />
          </Field>
          <Field label="Lieferanten-Bestellnummer">
            <input
              value={supplierOrderNumber}
              onChange={(event) => setSupplierOrderNumber(event.target.value)}
              placeholder="z. B. AB-4711"
            />
          </Field>
          {imported && (
            <>
              <Field label="Bestellstatus">
                <select value={status} onChange={(event) => setStatus(event.target.value)}>
                  <option value="sent">Gesendet</option>
                  <option value="confirmed">Bestätigt</option>
                </select>
              </Field>
              <Field label="Währung">
                <select value={currency} onChange={(event) => setCurrency(event.target.value)}>
                  <option value="EUR">EUR</option>
                  <option value="CHF">CHF</option>
                  <option value="USD">USD</option>
                  <option value="GBP">GBP</option>
                </select>
              </Field>
            </>
          )}
          <Field label="Notizen" full>
            <textarea
              value={notes}
              onChange={(event) => setNotes(event.target.value)}
            />
          </Field>
        </div>
        <h4>Positionen</h4>
        {lines.map((line, index) => (
          <div className="line-editor" key={index}>
            <div className="field description">
              <label>Artikel / Beschreibung</label>
              <select
                value={line.productId || ""}
                onChange={(event) =>
                  chooseProduct(index, Number(event.target.value))
                }
              >
                <option value="">Freitext</option>
                {products.map((product) => (
                  <option value={product.id} key={product.id}>
                    {product.sku} · {product.name}
                  </option>
                ))}
              </select>
              <input
                style={{ marginTop: ".35rem" }}
                required
                value={line.description}
                onChange={(event) =>
                  setLines((current) =>
                    current.map((item, lineIndex) =>
                      lineIndex === index
                        ? { ...item, description: event.target.value }
                        : item,
                    ),
                  )
                }
              />
            </div>
            <Field label="Menge">
              <input
                type="number"
                min="0.01"
                step="0.01"
                value={line.quantity}
                onChange={(event) =>
                  setLines((current) =>
                    current.map((item, lineIndex) =>
                      lineIndex === index
                        ? { ...item, quantity: Number(event.target.value) }
                        : item,
                    ),
                  )
                }
              />
            </Field>
            <Field label="Einheit">
              <input
                value={line.unit}
                onChange={(event) =>
                  setLines((current) =>
                    current.map((item, lineIndex) =>
                      lineIndex === index
                        ? { ...item, unit: event.target.value }
                        : item,
                    ),
                  )
                }
              />
            </Field>
            <Field label={`Preis ${currency}`}>
              <input
                type="number"
                min="0"
                step="0.01"
                value={line.unitPriceCents / 100}
                onChange={(event) =>
                  setLines((current) =>
                    current.map((item, lineIndex) =>
                      lineIndex === index
                        ? {
                            ...item,
                            unitPriceCents: Math.round(
                              Number(event.target.value) * 100,
                            ),
                          }
                        : item,
                    ),
                  )
                }
              />
            </Field>
            <Button
              className="icon"
              variant="danger"
              type="button"
              onClick={() =>
                setLines((current) =>
                  current.filter((_, lineIndex) => lineIndex !== index),
                )
              }
            >
              <X size={16} />
            </Button>
          </div>
        ))}
        <Button
          variant="ghost"
          type="button"
          onClick={() =>
            setLines((current) => [
              ...current,
              {
                description: "",
                quantity: 1,
                receivedQuantity: 0,
                unit: "Stk.",
                unitPriceCents: 0,
                purchaseUrl: "",
              },
            ])
          }
        >
          <Plus size={15} /> Position
        </Button>
      </form>
    </Modal>
  );
}

function SupplierOrderNumberEditor({ order, disabled, onSaved }: { order: Order; disabled: boolean; onSaved: () => Promise<void> }) {
  const [value, setValue] = useState(order.supplierOrderNumber || "");
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState("");
  const save = async () => {
    setSaving(true);
    setError("");
    try {
      await api(`/orders/${order.id}`, {
        method: "PUT",
        body: JSON.stringify({
          status: order.status,
          supplierOrderNumber: value,
          expectedDelivery: order.expectedDelivery || null,
          notes: order.notes || "",
        }),
      });
      await onSaved();
    } catch (caught) {
      setError((caught as Error).message);
    } finally {
      setSaving(false);
    }
  };
  return <div>
    <div className="receipt-link-row">
      <input value={value} disabled={disabled || saving} onChange={(event) => setValue(event.target.value)} placeholder="Bestellnummer des Lieferanten" />
      {!disabled && <Button type="button" variant="ghost" disabled={saving} onClick={() => void save()}>{saving ? "Speichert …" : "Speichern"}</Button>}
    </div>
    {error && <div className="notice">{error}</div>}
  </div>;
}

function ReceiptModal({
  value,
  onClose,
  onSaved,
}: {
  value: { order: Order; line: OrderLine };
  onClose: () => void;
  onSaved: () => void;
}) {
  const remaining = value.line.quantity - value.line.receivedQuantity;
  const procurementProduct = value.line.product;
  const [quantity, setQuantity] = useState(remaining);
  const [note, setNote] = useState("");
  const [error, setError] = useState("");
  const [warehouseProduct, setWarehouseProduct] = useState<Product | null>(
    procurementProduct?.warehouseProductId ? procurementProduct : null,
  );
  const [candidates, setCandidates] = useState<WarehouseProductCandidate[]>([]);
  const [selectedCandidate, setSelectedCandidate] = useState(0);
  const [checking, setChecking] = useState(false);
  const [linking, setLinking] = useState(false);
  const requiresWarehouseLink = Boolean(procurementProduct);
  const canSubmit = !requiresWarehouseLink || Boolean(warehouseProduct);

  const checkWarehouseLink = async () => {
    if (!procurementProduct) return;
    setChecking(true);
    setError("");
    try {
      const refreshed = await api<Product>(`/products/${procurementProduct.id}`);
      if (refreshed.warehouseProductId) {
        setWarehouseProduct(refreshed);
        setCandidates([]);
        setSelectedCandidate(0);
      } else {
        const matches = await api<WarehouseProductCandidate[]>(
          `/products/${procurementProduct.id}/warehouse-candidates`,
        );
        setWarehouseProduct(null);
        setCandidates(matches);
        setSelectedCandidate(matches[0]?.productId || 0);
      }
    } catch (caught) {
      setError((caught as Error).message);
    } finally {
      setChecking(false);
    }
  };

  useEffect(() => {
    if (procurementProduct && !procurementProduct.warehouseProductId) {
      void checkWarehouseLink();
    }
    // The receipt target does not change while this modal is mounted.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [procurementProduct?.id]);

  const linkCandidate = async () => {
    if (!procurementProduct || !selectedCandidate) return;
    setLinking(true);
    setError("");
    try {
      await api(`/products/${procurementProduct.id}/warehouse-link`, {
        method: "POST",
        body: JSON.stringify({ warehouseProductId: selectedCandidate }),
      });
      await checkWarehouseLink();
    } catch (caught) {
      setError((caught as Error).message);
    } finally {
      setLinking(false);
    }
  };

  const submit = async (event: FormEvent) => {
    event.preventDefault();
    if (!canSubmit) return;
    try {
      await api(`/orders/${value.order.id}/receipt`, {
        method: "POST",
        body: JSON.stringify({ lineId: value.line.id, quantity, note }),
      });
      onSaved();
    } catch (caught) {
      setError((caught as Error).message);
    }
  };

  const individual = warehouseProduct?.warehouseTrackingMode === "individual";
  return (
    <Modal
      title="Wareneingang verbuchen"
      onClose={onClose}
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            Abbrechen
          </Button>
          <Button
            variant="primary"
            type="submit"
            form="receipt-form"
            disabled={!canSubmit}
          >
            Verbuchen
          </Button>
        </>
      }
    >
      <form id="receipt-form" onSubmit={submit}>
        {error && <div className="notice">{error}</div>}
        <p>
          <strong>{value.line.description}</strong>
          <br />
          <span style={{ color: "var(--text-muted)" }}>
            Noch offen: {remaining} {value.line.unit}
          </span>
        </p>

        {!procurementProduct && (
          <div className="receipt-inventory">
            <strong>Freitextposition</strong>
            <span>
              Der Eingang wird dokumentiert, aber keinem Warehouse-Produkt
              zugeordnet.
            </span>
          </div>
        )}

        {procurementProduct && warehouseProduct && (
          <div className="receipt-inventory">
            <div className="receipt-inventory-heading">
              <div>
                <strong>{warehouseProduct.warehouseProductName}</strong>
                <span>{warehouseProduct.warehouseProductCode}</span>
              </div>
              <Badge tone="green">
                {trackingLabel(warehouseProduct.warehouseTrackingMode)}
              </Badge>
            </div>
            <p>{receiptInventoryMessage(warehouseProduct, quantity)}</p>
            <a
              className="btn ghost"
              href={warehouseProductsURL({
                product_id: warehouseProduct.warehouseProductId!,
              })}
              target="_blank"
              rel="noreferrer"
            >
              <ExternalLink size={15} /> Warehouse-Produkt öffnen
            </a>
          </div>
        )}

        {procurementProduct && !warehouseProduct && (
          <div className="receipt-inventory">
            <div className="receipt-inventory-heading">
              <div>
                <strong>Warehouse-Produkt fehlt</strong>
                <span>
                  Vor dem Verbuchen ein bestehendes Produkt verknüpfen oder neu
                  anlegen.
                </span>
              </div>
              <Button
                className="icon"
                variant="ghost"
                type="button"
                onClick={() => void checkWarehouseLink()}
                disabled={checking}
                aria-label="Warehouse-Verknüpfung erneut prüfen"
                title="Erneut prüfen"
              >
                <RefreshCw className={checking ? "spin" : ""} size={15} />
              </Button>
            </div>
            {candidates.length > 0 && (
              <div className="receipt-link-row">
                <select
                  aria-label="Bestehendes Warehouse-Produkt"
                  value={selectedCandidate}
                  onChange={(event) =>
                    setSelectedCandidate(Number(event.target.value))
                  }
                >
                  {candidates.map((candidate) => (
                    <option value={candidate.productId} key={candidate.productId}>
                      {candidate.productCode} · {candidate.name} · {candidate.score}
                      Punkte
                    </option>
                  ))}
                </select>
                <Button
                  variant="ghost"
                  type="button"
                  disabled={!selectedCandidate || linking}
                  onClick={() => void linkCandidate()}
                >
                  <Link2 size={15} /> {linking ? "Wird verknüpft …" : "Verknüpfen"}
                </Button>
              </div>
            )}
            {candidates.length === 0 && !checking && (
              <span>Kein passender bestehender Artikel erkannt.</span>
            )}
            <a
              className="btn primary"
              href={warehouseProductsURL({
                procurement_product_id: procurementProduct.id,
              })}
              target="_blank"
              rel="noreferrer"
            >
              <ExternalLink size={15} /> Produkt in WarehouseCore anlegen
            </a>
          </div>
        )}

        <div className="form-grid">
          <Field label="Eingangsmenge">
            <input
              type="number"
              min={individual ? 1 : 0.01}
              max={remaining}
              step={individual ? 1 : 0.01}
              value={quantity}
              onChange={(event) => setQuantity(Number(event.target.value))}
            />
          </Field>
          <Field label="Notiz">
            <input
              value={note}
              onChange={(event) => setNote(event.target.value)}
            />
          </Field>
        </div>
      </form>
    </Modal>
  );
}
