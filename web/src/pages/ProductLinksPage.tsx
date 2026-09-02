import { useEffect, useMemo, useState } from "react";
import { ExternalLink, Link2, RefreshCw, Search, Unlink } from "lucide-react";
import { api } from "../lib/api";
import { warehouseProductsURL } from "../lib/app-paths";
import type {
  ProductLinkOverview,
  WarehouseProductCandidate,
} from "../lib/types";
import { Badge, Button, Empty } from "../components/ui";
import { useApp } from "../App";

type LinkResponse = {
  items: ProductLinkOverview[];
  warehouseProducts: WarehouseProductCandidate[];
};

export default function ProductLinksPage() {
  const { user, notify } = useApp();
  const [data, setData] = useState<LinkResponse>({
    items: [],
    warehouseProducts: [],
  });
  const [loading, setLoading] = useState(true);
  const [query, setQuery] = useState("");
  const [selection, setSelection] = useState<Record<number, number>>({});

  const load = async () => {
    setLoading(true);
    try {
      const result = await api<LinkResponse>("/product-links");
      setData(result);
      setSelection(
        Object.fromEntries(
          result.items
            .filter((item) => !item.warehouseProductId && item.candidates[0])
            .map((item) => [
              item.procurementProductId,
              item.candidates[0].productId,
            ]),
        ),
      );
    } finally {
      setLoading(false);
    }
  };
  useEffect(() => {
    void load();
  }, []);

  const rows = useMemo(() => {
    const needle = query.trim().toLowerCase();
    if (!needle) return data.items;
    return data.items.filter((item) =>
      `${item.sku} ${item.name} ${item.manufacturer} ${item.model} ${item.warehouseProduct?.name || ""}`
        .toLowerCase()
        .includes(needle),
    );
  }, [data.items, query]);

  const link = async (item: ProductLinkOverview) => {
    const warehouseProductId = selection[item.procurementProductId];
    if (!warehouseProductId) return;
    await api(`/products/${item.procurementProductId}/warehouse-link`, {
      method: "POST",
      body: JSON.stringify({ warehouseProductId }),
    });
    notify("Produkte wurden verknüpft");
    await load();
  };
  const unlink = async (item: ProductLinkOverview) => {
    if (!confirm(`Verknüpfung für „${item.name}“ wirklich lösen?`)) return;
    await api(`/products/${item.procurementProductId}/warehouse-link`, {
      method: "DELETE",
    });
    notify("Verknüpfung wurde gelöst");
    await load();
  };

  return (
    <div className="content">
      <div className="page-header">
        <div>
          <h2>Produktabgleich</h2>
          <p>
            Bestehende Einkaufsartikel eindeutig mit dem physischen
            Warehouse-Produktstamm verbinden.
          </p>
        </div>
        <Button variant="ghost" onClick={() => void load()}>
          <RefreshCw size={16} /> Aktualisieren
        </Button>
      </div>
      <div className="toolbar">
        <div className="search suite-search-field">
          <Search size={17} />
          <input
            value={query}
            onChange={(event) => setQuery(event.target.value)}
            placeholder="Procurement- oder Warehouse-Produkt suchen …"
          />
        </div>
        <Badge tone="accent">
          {data.items.filter((item) => item.warehouseProductId).length} von{" "}
          {data.items.length} verknüpft
        </Badge>
      </div>
      {loading ? (
        <div className="empty">Produktstämme werden abgeglichen …</div>
      ) : rows.length === 0 ? (
        <Empty>Keine Produkte gefunden.</Empty>
      ) : (
        <div className="suite-table-wrap">
          <table>
            <thead>
              <tr>
                <th>ProcurementCore</th>
                <th>WarehouseCore</th>
                <th>Treffergrund</th>
                <th></th>
              </tr>
            </thead>
            <tbody>
              {rows.map((item) => {
                const selectedID = selection[item.procurementProductId];
                const selectedCandidate =
                  item.candidates.find(
                    (product) => product.productId === selectedID,
                  ) ||
                  data.warehouseProducts.find(
                    (product) => product.productId === selectedID,
                  );
                return (
                  <tr key={item.procurementProductId}>
                    <td>
                      <div className="cell-title">{item.name}</div>
                      <div className="cell-sub">
                        {item.sku} ·{" "}
                        {[item.manufacturer, item.model]
                          .filter(Boolean)
                          .join(" ") || "ohne Hersteller/Modell"}
                      </div>
                    </td>
                    <td>
                      {item.warehouseProduct ? (
                        <>
                          <div className="cell-title">
                            {item.warehouseProduct.name}
                          </div>
                          <div className="cell-sub">
                            {item.warehouseProduct.productCode}
                          </div>
                        </>
                      ) : (
                        <select
                          value={selectedID || ""}
                          onChange={(event) =>
                            setSelection((current) => ({
                              ...current,
                              [item.procurementProductId]: Number(
                                event.target.value,
                              ),
                            }))
                          }
                        >
                          <option value="">Warehouse-Produkt wählen …</option>
                          {data.warehouseProducts
                            .filter((product) => !product.procurementProductId)
                            .map((product) => (
                              <option
                                key={product.productId}
                                value={product.productId}
                              >
                                {product.productCode} · {product.name}
                              </option>
                            ))}
                        </select>
                      )}
                    </td>
                    <td>
                      {item.warehouseProduct ? (
                        <Badge tone="success">Verknüpft</Badge>
                      ) : selectedCandidate ? (
                        <>
                          <Badge
                            tone={
                              selectedCandidate.score >= 80
                                ? "success"
                                : "warning"
                            }
                          >
                            {selectedCandidate.score} Punkte
                          </Badge>
                          <div className="cell-sub">
                            {selectedCandidate.reasons.join(" · ") ||
                              "manuell gewählt"}
                          </div>
                        </>
                      ) : (
                        <span className="cell-sub">Kein Produkt gewählt</span>
                      )}
                    </td>
                    <td>
                      <div className="row-actions">
                        {item.warehouseProductId ? (
                          <>
                            <a
                              className="btn ghost"
                              href={warehouseProductsURL({
                                product_id: item.warehouseProductId,
                              })}
                            >
                              <ExternalLink size={15} /> Öffnen
                            </a>
                            {user.isAdmin && (
                              <Button
                                className="icon"
                                variant="danger"
                                onClick={() => void unlink(item)}
                                title="Verknüpfung lösen"
                              >
                                <Unlink size={15} />
                              </Button>
                            )}
                          </>
                        ) : (
                          <>
                            {user.isAdmin && (
                              <Button
                                variant="ghost"
                                disabled={!selectedID}
                                onClick={() => void link(item)}
                              >
                                <Link2 size={15} /> Verknüpfen
                              </Button>
                            )}
                            {user.isAdmin && (
                              <a
                                className="btn primary"
                                href={warehouseProductsURL({
                                  procurement_product_id:
                                    item.procurementProductId,
                                })}
                              >
                                <ExternalLink size={15} /> Neu übernehmen
                              </a>
                            )}
                          </>
                        )}
                      </div>
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      )}
    </div>
  );
}
