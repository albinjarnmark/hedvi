import { supportEmail } from "./public-site";
// Public product copy in English. Claims follow brand/README.md:
// no deliverability, uptime, compliance or price-comparison claims before
// they are verified.
export type Locale = "en";

export const dict = {
  en: {
    nav: { docs: "Docs", pricing: "Pricing", switch: "Switch from Resend", signin: "Sign in", start: "Start testing" },
    status: "Open source · Hosted in the EU",
    hero: {
      title: "Email, without the overhead.",
      lead: "A simpler API for transactional email: test safely, understand delivery and control cost. Made in Sweden, for developers everywhere.",
      cta: "Send your first test email",
      secondary: "Read the quickstart",
    },
    pillars: [
      { title: "Test safely", text: "Every project starts with a test key and a test inbox. Test mail is captured, never delivered, and the key's mode is checked in the database — not from its prefix." },
      { title: "Understand delivery", text: "Queued, accepted by the provider and the receiving server's answer are separate steps per recipient. “Delivered” means the server accepted it, not inbox placement." },
      { title: "Control cost", text: "Get alerts at 80% and 95% of included quota. Use prepaid credits when you choose, and stop sending when eligible units run out." },
    ],
    flowTitle: "One path from test to production",
    flow: ["Test", "Add domain", "Verify automatically", "Send", "Follow each recipient"],
    honestyTitle: "What we promise today",
    honesty: [
      "Email payload storage and sending use AWS Stockholm. The app, database and backups use Railway in Amsterdam. See our data and retention guide.",
      "Accepted means durably stored: we answer only after the email, its quota reservation and its send job are committed.",
      "If the provider's answer is ambiguous we show “delivery unknown” instead of retrying blindly and risking a duplicate.",
    ],
    codeTitle: "Works with the Resend SDKs you already use",
    codeNote: "A documented subset of the Resend API. Set the base URL and use a Hedvi key. See exactly what is supported.",
    footer: { company: "Hedvi is a service from StackX AB, Sweden.", contact: supportEmail },
    pricing: {
      title: "Three simple choices",
      lead: "Prices before VAT. When hosted billing is enabled, Stripe Checkout shows your currency, applicable tax and total before payment.",
      free: { name: "Free", price: "0", per: "", points: ["Up to 5,000 recipients/month after domain verification", "50 recipients/hour · 200/day", "3 domains", "Test inbox", "14 days metadata, up to 7 days content"] },
      flex: { name: "Flex", price: "$4", per: "per 10k recipients", points: ["Prepaid credits, no subscription", "No hourly or daily plan limit", "Valid for 365 days, expiry always visible", "Up to 10 domains", "30 days metadata, 7 days content"] },
      pro: { name: "Pro", price: "$13", per: "/month for 50k · $25 for 100k", points: ["No hourly or daily plan limit", "Signed webhooks and test inbox", "25 or 100 domains", "90 days metadata, 7 days content", "Optional use of prepaid Flex credits"] },
      larger: "Larger volume: quote for now.",
      notes: ["A recipient is each unique address in To, Cc and Bcc.", "Your application’s sending stops when eligible units run out, including its sign-in and password emails. Warnings appear at 80% and 95% of included quota.", "Pro 50k includes 1 GiB of attachment data per paid invoice cycle."],
    },
  },
} as const;

type Widen<T> = T extends string ? string : T extends readonly (infer U)[] ? readonly Widen<U>[] : { readonly [K in keyof T]: Widen<T[K]> };
export type Dict = Widen<(typeof dict)["en"]>;
