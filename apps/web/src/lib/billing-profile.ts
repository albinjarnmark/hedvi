export type BillingProfile = {
  name: string;
  line1: string;
  line2: string;
  city: string;
  postal_code: string;
  state: string;
  country: string;
};

export function billingProfileFromForm(form: FormData): BillingProfile {
  const field = (name: string) => String(form.get(name) ?? "").trim();
  return {
    name: field("name"), line1: field("line1"), line2: field("line2"),
    city: field("city"), postal_code: field("postal_code"),
    state: field("state"), country: field("country").toUpperCase(),
  };
}
