import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import type { OrderImportPreview, Product, Supplier } from "../lib/types";
import { ADAM_HALL_CART_URL, AdamHallCartLink, isAdamHallSupplier } from "../components/AdamHallOrderModal";
import { OrderModal, PDFOrderImportModal, receiptInventoryMessage } from "./OrdersPage";

const product = (values: Partial<Product>): Product =>
  ({
    id: 1,
    sku: "TEST",
    name: "Testprodukt",
    description: "",
    unit: "Stk.",
    manufacturer: "",
    model: "",
    parameters: {},
    attributes: {},
    active: true,
    reorderPoint: 0,
    targetStock: 0,
    offers: [],
    ...values,
  }) as Product;

describe("receiptInventoryMessage", () => {
  it("shows the resulting quantity stock", () => {
    expect(
      receiptInventoryMessage(
        product({
          warehouseTrackingMode: "quantity",
          warehouseStockQuantity: 2,
        }),
        8,
      ),
    ).toContain("von 2 auf 10");
  });

  it("shows the resulting device count", () => {
    expect(
      receiptInventoryMessage(
        product({
          warehouseTrackingMode: "individual",
          warehouseDeviceCount: 2,
        }),
        8,
      ),
    ).toContain("10 Devices");
  });
});

describe("isAdamHallSupplier", () => {
  const supplier = (values: Partial<import("../lib/types").Supplier>) => ({
    id: 1, name: "", code: "", website: "", contactName: "", email: "", phone: "",
    paymentTerms: "", defaultLeadDays: 0, rating: 0, preferred: false, active: true,
    riskLevel: "low" as const, notes: "", ...values,
  });

  it("recognizes Adam Hall by name or official host", () => {
    expect(isAdamHallSupplier(supplier({ name: "Adam Hall GmbH" }))).toBe(true);
    expect(isAdamHallSupplier(supplier({ website: "https://www.adamhall.com/shop/de" }))).toBe(true);
  });

  it("rejects lookalike hosts", () => {
    expect(isAdamHallSupplier(supplier({ website: "https://adamhall.com.example.org" }))).toBe(false);
  });
});

describe("AdamHallCartLink", () => {
  it("opens the official cart safely in a new tab", () => {
    const markup = renderToStaticMarkup(createElement(AdamHallCartLink));

    expect(ADAM_HALL_CART_URL).toBe("https://www.adamhall.com/shop/de/checkout/cart");
    expect(markup).toContain(`href="${ADAM_HALL_CART_URL}"`);
    expect(markup).toContain('target="_blank"');
    expect(markup).toContain('rel="noopener noreferrer"');
  });
});

describe("PDF order import", () => {
  const supplier = {
    id: 7, name: "Adam Hall GmbH", code: "AH", website: "https://www.adamhall.com",
    contactName: "", email: "", phone: "", paymentTerms: "", defaultLeadDays: 0,
    rating: 0, preferred: true, active: true, riskLevel: "low", notes: "",
  } as Supplier;

  it("offers a PDF-only upload before analysis", () => {
    const markup = renderToStaticMarkup(createElement(PDFOrderImportModal, {
      onClose: () => undefined,
      onPreview: () => undefined,
    }));

    expect(markup).toContain("Bestellung aus PDF");
    expect(markup).toContain('type="file"');
    expect(markup).toContain('accept="application/pdf,.pdf"');
    expect(markup).toContain("Die PDF wird nicht dauerhaft gespeichert");
  });

  it("renders extracted values in the editable order form", () => {
    const preview: OrderImportPreview = {
      sourceFileName: "order.pdf", pageCount: 2, extractedCharacters: 900,
      supplierId: supplier.id, supplierName: supplier.name, supplierOrderNumber: "INV-42",
      orderDate: "2026-09-08T12:00:00Z", expectedDelivery: "2026-09-12T12:00:00Z",
      currency: "EUR", documentTotalCents: 1250, recognizedTotalCents: 1250,
      confidence: 87, warnings: ["Preis prüfen"],
      lines: [{ description: "Patchkabel", quantity: 5, receivedQuantity: 0, unit: "Stk.", unitPriceCents: 250, purchaseUrl: "" }],
    };
    const markup = renderToStaticMarkup(createElement(OrderModal, {
      suppliers: [supplier], products: [], initial: preview,
      onClose: () => undefined, onSaved: () => undefined,
    }));

    expect(markup).toContain("PDF-Bestellung prüfen");
    expect(markup).toContain("87% Erkennungsgrad");
    expect(markup).toContain('value="INV-42"');
    expect(markup).toContain('value="2026-09-08"');
    expect(markup).toContain("Importierte Bestellung anlegen");
  });
});
