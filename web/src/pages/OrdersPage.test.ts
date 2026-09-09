import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import type { Product } from "../lib/types";
import { ADAM_HALL_CART_URL, AdamHallCartLink, isAdamHallSupplier } from "../components/AdamHallOrderModal";
import { receiptInventoryMessage } from "./OrdersPage";

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
