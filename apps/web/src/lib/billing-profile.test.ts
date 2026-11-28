import { describe, expect, it } from "vitest";
import { billingProfileFromForm } from "./billing-profile";

describe("company invoice address", () => {
  it("preserves Unicode and every invoice field independently of the payment method", () => {
    const form = new FormData();
    Object.entries({name: " Örebro Test AB ", line1: " Storgatan 1 ", line2: "", city: "Örebro", postal_code: "701 10", state: "", country: "se", card_country: "US"}).forEach(([k,v]) => form.set(k,v));
    expect(billingProfileFromForm(form)).toEqual({name: "Örebro Test AB", line1: "Storgatan 1", line2: "", city: "Örebro", postal_code: "701 10", state: "", country: "SE"});
  });
});
