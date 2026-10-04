// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { api } from "../lib/api";
import type { Order } from "../lib/types";
import AdamHallOrderModal from "./AdamHallOrderModal";

vi.mock("../lib/api", () => ({ api: vi.fn() }));
const request = vi.mocked(api);
const order = { id: 7, number: "AH-7" } as Order;
const cart = {
  lines: [{ productNumber: "SKU-1", description: "Business product", quantity: 2, unitPriceCents: 1235, totalCents: 2470 }],
  currency: "EUR", totalCents: 2470, customer: "Configured business account",
  shippingAddress: "Delivery GmbH, Delivery street 1", billingAddress: "Billing GmbH, Billing street 2",
  shippingMethod: "Business delivery", paymentMethod: "Invoice",
};
const review = {
  preview: true, ready_to_execute: true, expected_updated_at: "2026-10-04T12:00:00.123456Z",
  expected_context: "a".repeat(64), required_confirmation_text: "BUILD ADAM HALL CART ORDER 7 aaaaaaaaaaaaaaaa",
  current: order, supplier_items: [{ productNumber: "SKU-1", quantity: 2 }],
};
const paidReview = {
  ...review, expected_context: "b".repeat(64), checkout_id: 9, supplier_cart: cart,
  required_confirmation_text: "SEND ADAM HALL ORDER 7 EUR 2470 CHECKOUT 9 bbbbbbbbbbbbbbbb",
};
const mount = () => render(<AdamHallOrderModal order={order} onClose={vi.fn()} onOrdered={vi.fn()} />);

beforeEach(() => { request.mockReset(); localStorage.setItem("cores_language", "de"); });
afterEach(cleanup);

describe("Adam Hall confirmed checkout", () => {
  it("opens with a pure GET and requires separate cart and paid confirmations", async () => {
    request.mockResolvedValueOnce(review).mockResolvedValueOnce({ operation_status: "ready", checkout_id: 9, supplier_cart: cart })
      .mockResolvedValueOnce(paidReview).mockResolvedValueOnce({ order, cart });
    const ordered = vi.fn();
    render(<AdamHallOrderModal order={order} onClose={vi.fn()} onOrdered={ordered} />);
    await screen.findByText("SKU-1");
    expect(request.mock.calls).toEqual([["/orders/7/adam-hall/review"]]);
    expect((screen.getByRole("button", { name: "Warenkorb vorbereiten" }) as HTMLButtonElement).disabled).toBe(true);
    fireEvent.click(screen.getByRole("checkbox"));
    fireEvent.click(screen.getByRole("button", { name: "Warenkorb vorbereiten" }));
    await screen.findByText(cart.billingAddress);
    expect(request.mock.calls[2]).toEqual(["/orders/7/adam-hall/send-review"]);
    expect((screen.getByRole("checkbox") as HTMLInputElement).checked).toBe(false);
    expect((screen.getByRole("button", { name: "Verbindlich bestellen" }) as HTMLButtonElement).disabled).toBe(true);
    fireEvent.click(screen.getByRole("checkbox"));
    fireEvent.click(screen.getByRole("button", { name: "Verbindlich bestellen" }));
    await waitFor(() => expect(ordered).toHaveBeenCalledWith({ order, cart }));
    const build = request.mock.calls[1][1]!;
    const send = request.mock.calls[3][1]!;
    expect(JSON.parse(build.body as string)).toMatchObject({ expected_context: review.expected_context, confirmation_text: review.required_confirmation_text, confirm_change: true });
    expect(JSON.parse(send.body as string)).toMatchObject({ checkout_id: 9, expected_context: paidReview.expected_context, confirmation_text: paidReview.required_confirmation_text, expected_updated_at: paidReview.expected_updated_at, confirm_change: true });
    expect(new Headers(build.headers).get("Idempotency-Key")).toBeTruthy();
    expect(new Headers(send.headers).get("Idempotency-Key")).not.toBe(new Headers(build.headers).get("Idempotency-Key"));
  });

  it("keeps the exact paid request/key after a lost response and never retries automatically", async () => {
    request.mockResolvedValueOnce(review).mockResolvedValueOnce({ operation_status: "ready", checkout_id: 9 })
      .mockResolvedValueOnce(paidReview).mockRejectedValueOnce(new Error("Connection lost"))
      .mockResolvedValueOnce({ order, cart });
    mount();
    await screen.findByText("SKU-1");
    fireEvent.click(screen.getByRole("checkbox"));
    fireEvent.click(screen.getByRole("button", { name: "Warenkorb vorbereiten" }));
    await screen.findByText(cart.billingAddress);
    fireEvent.click(screen.getByRole("checkbox"));
    fireEvent.click(screen.getByRole("button", { name: "Verbindlich bestellen" }));
    await screen.findByText("Connection lost");
    expect(request).toHaveBeenCalledTimes(4);
    fireEvent.click(screen.getByRole("button", { name: "Gespeicherten Vorgang prüfen" }));
    await waitFor(() => expect(request).toHaveBeenCalledTimes(5));
    expect(request.mock.calls[4]).toEqual(request.mock.calls[3]);
  });

  it("stops at a pending cart outcome without a paid call", async () => {
    request.mockResolvedValueOnce(review).mockResolvedValueOnce({ operation_status: "pending", checkout_id: 9 });
    mount();
    await screen.findByText("SKU-1");
    fireEvent.click(screen.getByRole("checkbox"));
    fireEvent.click(screen.getByRole("button", { name: "Warenkorb vorbereiten" }));
    await screen.findByText(/Es wurde keine kostenpflichtige Bestellung ausgelöst/);
    expect(request).toHaveBeenCalledTimes(2);
    expect(screen.queryByRole("checkbox")).toBeNull();
    expect((screen.getByRole("button", { name: "Warenkorb vorbereiten" }) as HTMLButtonElement).disabled).toBe(true);
  });

  it("shows English business copy and rejects a blocked preview", async () => {
    localStorage.setItem("cores_language", "en");
    request.mockResolvedValueOnce({ ...review, ready_to_execute: false });
    mount();
    await screen.findByText(/This preview cannot be confirmed/);
    expect(screen.queryByRole("checkbox")).toBeNull();
    expect((screen.getByRole("button", { name: "Prepare cart" }) as HTMLButtonElement).disabled).toBe(true);
    expect(request).toHaveBeenCalledTimes(1);
  });

  it("renders an empty blocked draft without a confirmation or a crash", async () => {
    request.mockResolvedValueOnce({ ...review, ready_to_execute: false, supplier_items: null });
    mount();
    await screen.findByText(/Diese Vorschau kann nicht bestätigt werden/);
    expect(screen.queryByRole("checkbox")).toBeNull();
    expect((screen.getByRole("button", { name: "Warenkorb vorbereiten" }) as HTMLButtonElement).disabled).toBe(true);
  });

  it("does not report a stale paid confirmation as a placed order", async () => {
    request.mockResolvedValueOnce(review).mockResolvedValueOnce({ operation_status: "ready", checkout_id: 9 })
      .mockResolvedValueOnce(paidReview).mockResolvedValueOnce({ ...paidReview, ready_to_execute: false });
    const ordered = vi.fn();
    render(<AdamHallOrderModal order={order} onClose={vi.fn()} onOrdered={ordered} />);
    await screen.findByText("SKU-1");
    fireEvent.click(screen.getByRole("checkbox"));
    fireEvent.click(screen.getByRole("button", { name: "Warenkorb vorbereiten" }));
    await screen.findByText(cart.billingAddress);
    fireEvent.click(screen.getByRole("checkbox"));
    fireEvent.click(screen.getByRole("button", { name: "Verbindlich bestellen" }));
    await screen.findByText(/Die geprüfte Bestellung hat sich geändert/);
    expect(ordered).not.toHaveBeenCalled();
    expect(screen.queryByRole("checkbox")).toBeNull();
  });
});
