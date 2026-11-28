import { supportEmail } from "./public-site";
// Marketing copy in English (main) and Swedish. Claims follow brand/README.md:
// no deliverability, uptime, compliance or price-comparison claims before
// they are verified. Prices are targets, not published offers.
export type Locale = "en" | "sv";

export const dict = {
  en: {
    nav: { docs: "Docs", pricing: "Pricing", switch: "Switch from Resend", signin: "Sign in", start: "Start testing", other: "Svenska", otherHref: "/sv" },
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
    flow: ["Test", "Add domain", "Get approved", "Send", "Follow each recipient"],
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
      flex: { name: "Flex", price: "$4", per: "per 10k recipients", points: ["Prepaid credits, no subscription", "100 recipients/hour · 1,000/day", "Valid for 365 days, expiry always visible", "Up to 10 domains", "30 days metadata, 7 days content"] },
      pro: { name: "Pro", price: "$13", per: "/month for 50k · $25 for 100k", points: ["50k: 250/hour · 2,500/day; 100k: 500/hour · 5,000/day", "Organization roles, webhooks, test inbox", "25 or 100 domains", "90 days metadata, 7 days content", "Optional use of prepaid Flex credits"] },
      larger: "Larger volume: quote for now.",
      notes: ["A recipient is each unique address in To, Cc and Bcc.", "Your application’s sending stops when eligible units run out, including its sign-in and password emails. Warnings appear at 80% and 95% of included quota.", "Pro 50k includes 1 GiB of attachment data per paid invoice cycle."],
    },
  },
  sv: {
    nav: { docs: "Dokumentation", pricing: "Priser", switch: "Byt från Resend", signin: "Logga in", start: "Börja testa", other: "English", otherHref: "/" },
    status: "Öppen källkod · Drift inom EU",
    hero: {
      title: "Email, without the overhead.",
      lead: "Ett enklare API för transaktionell e-post: testa tryggt, förstå leveransen och kontrollera kostnaden. Byggt i Sverige, för utvecklare överallt.",
      cta: "Skicka ditt första testmail",
      secondary: "Läs snabbstarten",
    },
    pillars: [
      { title: "Testa tryggt", text: "Varje projekt får en testnyckel och en testinkorg. Testmail fångas och levereras aldrig, och nyckelns läge kontrolleras i databasen — inte på prefixet." },
      { title: "Förstå leveransen", text: "Köad, accepterad av sändningsleverantören och mottagarserverns svar visas som separata steg per mottagare. ”Delivered” betyder att servern tog emot mailet, inte inkorgsplacering." },
      { title: "Kontrollera kostnaden", text: "Få varningar vid 80 och 95 procent. Välj om förbetalda krediter ska användas när den inkluderade kvoten tar slut." },
    ],
    flowTitle: "En väg från test till produktion",
    flow: ["Testa", "Lägg till domän", "Bli godkänd", "Skicka", "Följ varje mottagare"],
    honestyTitle: "Det här lovar vi i dag",
    honesty: [
      "Mailinnehåll lagras och skickas via AWS i Stockholm. Appen, databasen och säkerhetskopiorna finns hos Railway i Amsterdam. Läs guiden om data och lagring.",
      "Accepterat betyder varaktigt sparat: vi svarar först när mailet, kvotreservationen och sändningsjobbet är sparade.",
      "Om leverantörens svar är osäkert visar vi ”leverans okänd” i stället för att skicka om i blindo och riskera dubbletter.",
    ],
    codeTitle: "Fungerar med Resends SDK:er du redan använder",
    codeNote: "En dokumenterad delmängd av Resends API. Byt bas-URL och använd en Hedvi-nyckel. Se exakt vad som stöds.",
    footer: { company: "Hedvi är en tjänst från StackX AB, Sverige.", contact: supportEmail },
    pricing: {
      title: "Tre enkla val",
      lead: "Priser före moms. När betalning är aktiverad visar Stripe Checkout valuta, skatt och totalsumma innan du betalar.",
      free: { name: "Free", price: "0 kr", per: "", points: ["Upp till 5 000 mottagare/mån efter verifierad domän", "50 mottagare/timme · 200/dag", "3 domäner", "Testinkorg", "14 dagars metadata, högst 7 dagars innehåll"] },
      flex: { name: "Flex", price: "39 kr", per: "per 10 000 mottagare", points: ["Förbetalda krediter utan abonnemang", "100 mottagare/timme · 1 000/dag", "Giltiga i 365 dagar, utgångsdatum syns alltid", "Upp till 10 domäner", "30 dagars metadata, 7 dagars innehåll"] },
      pro: { name: "Pro", price: "129 kr", per: "/mån för 50 000 · 249 kr för 100 000", points: ["50k: 250/timme · 2 500/dag. 100k: 500/timme · 5 000/dag", "Organisationsroller, webhooks, testinkorg", "25 eller 100 domäner", "90 dagars metadata, 7 dagars innehåll", "Använd förbetalda Flex-krediter vid behov"] },
      larger: "Större volym: offert tills vidare.",
      notes: ["En mottagare är varje unik adress i To, Cc och Bcc.", "När tillgängliga enheter tar slut stoppas din applikations utskick, även dess inloggnings- och lösenordsmejl. Du får varningar vid 80 och 95 procent av den inkluderade kvoten.", "Pro 50k inkluderar 1 GiB bilagedata per betald fakturaperiod."],
    },
  },
} as const;

type Widen<T> = T extends string ? string : T extends readonly (infer U)[] ? readonly Widen<U>[] : { readonly [K in keyof T]: Widen<T[K]> };
export type Dict = Widen<(typeof dict)["en"]>;
