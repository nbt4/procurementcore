import { describe, expect, it } from "vitest";
import type { Product } from "../lib/types";
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
