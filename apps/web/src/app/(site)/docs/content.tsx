import { AgentSetup } from "@/components/AgentSetup";
import { ScrollTable } from "@/components/ScrollTable";
import { appLink } from "@/lib/navigation";
import Link from "next/link";
import { CodeBlock } from "@/components/CopyButton";
import { CodeTabs } from "@/components/CodeTabs";
import { sendSnippets } from "@/lib/snippets";
import { publicApiUrl } from "@/lib/public-site";
import { DocFigure } from "@/components/DocFigure";
import { guides } from "./guides";

const API = publicApiUrl;

export type Doc = { slug: string; title: string; body: React.ReactNode };

const verifyNode = `import { Resend } from "resend";

const resend = new Resend(process.env.HEDVI_API_KEY);

export async function POST(req: Request) {
  const payload = await req.text(); // the raw body, unparsed
  try {
    const event = resend.webhooks.verify({
      payload,
      headers: {
        id: req.headers.get("webhook-id")!,
        timestamp: req.headers.get("webhook-timestamp")!,
        signature: req.headers.get("webhook-signature")!,
      },
      webhookSecret: process.env.HEDVI_WEBHOOK_SECRET!, // whsec_…
    });
    // Deduplicate on webhook-id: delivery is at least once.
    return new Response(null, { status: 204 });
  } catch {
    return new Response("invalid signature", { status: 400 });
  }
}`;

export const docs: Doc[] = [
  { slug: "agent-setup", title: "Set up with your agent", body: <><p>Use your coding agent to get started with Hedvi in an existing application. Copy the prompt below into its chat and let it inspect your project.</p><AgentSetup /><h2>What you do yourself</h2><p>Complete account verification, choose your password and review the service terms. Enter API keys directly into your secret manager. Your agent can prepare the integration while you finish these steps.</p><h2>Start in the test inbox</h2><p>A test key lets you validate the integration before adding a domain or changing your existing email provider. Go live only after domain verification and automatic sending activation.</p></> },
  {
    slug: "quickstart",
    title: "Quickstart",
    body: (
      <>
        <p>Verify your email and complete signup. Hedvi creates a project with a <strong>test key</strong>. Test emails are captured in your test inbox and never leave Hedvi, so you can start before you own a domain.</p>
        <DocFigure name="first-email" alt="Create an account, copy the key, send an API request and inspect the test inbox." caption="Four steps to a captured test email, without a sending domain." />
        <ol>
          <li><Link href={appLink("/signup")}>Request a signup link</Link>, verify your mailbox, complete the form and copy the test key shown once. The <Link href="/docs/account-and-security">account guide</Link> explains the verification and document review.</li>
          <li>Send a request (any <code>from</code> address works in test mode):</li>
        </ol>
        <CodeTabs snippets={sendSnippets(API, "hv_test_…", "Acme <onboarding@example.com>", "you@example.com")} />
        <p style={{ marginTop: 16 }}>The response is <code>{`{"id": "…"}`}</code>. The email appears in <strong>Test inbox</strong> with HTML preview, text, headers and attachments. It is a <em>test email</em>: nothing was delivered to an external mailbox.</p>
        <p>Prefer help from your coding agent? <Link href="/docs/agent-setup">Copy the setup prompt</Link>.</p>
        <h2>Go live</h2>
        <ol>
          <li>Add your domain under <strong>Domains</strong>, publish the ownership TXT record, and choose <strong>Check now</strong>.</li>
          <li>Publish the DKIM and MAIL FROM records that appear after the ownership check, then choose <strong>Check now</strong> again. Sending activates automatically once DNS and Amazon SES confirm your domain. No separate application or manual approval is needed.</li>
          <li>Create a live key under <strong>Developers</strong> and send from your verified domain.</li>
        </ol>
      </>
    ),
  },
  {
    slug: "sending",
    title: "Sending email",
    body: (
      <>
        <p><code>POST /emails</code> accepts a documented subset of Resend&apos;s request. Hedvi answers <code>200</code> only after the email, its recipients, its quota reservation and its send job are durably stored.</p>
    <DocFigure name="api-request" alt="Use a project key, submit the email and track its ID." caption="Inspect the recorded result after submitting a message." />
        <ScrollTable>
          <thead><tr><th>Field</th><th>Notes</th></tr></thead>
          <tbody>
            <tr><td><code>from</code></td><td><code>email@domain</code> or <code>Name &lt;email@domain&gt;</code>. Live keys require a verified domain in the key&apos;s project. Display names are limited to 128 UTF-8 bytes; quoted local-parts are unsupported.</td></tr>
            <tr><td><code>to</code>, <code>cc</code>, <code>bcc</code></td><td>String or array. At most 50 recipients in total. Bcc is never written to headers.</td></tr>
            <tr><td><code>subject</code></td><td>Required, one line, up to 989 UTF-8 bytes.</td></tr>
            <tr><td><code>html</code>, <code>text</code></td><td>At least one. UTF-8.</td></tr>
            <tr><td><code>reply_to</code></td><td>String or array, at most 50 addresses.</td></tr>
            <tr><td><code>headers</code></td><td>Custom headers such as <code>List-Unsubscribe</code>. Structural headers (From, To, Content-Type …) and the X-SES-, X-AMZ-, X-Hedvi- and Resent- prefixes are refused.</td></tr>
            <tr><td><code>tags</code></td><td>Up to 10 <code>{`{name, value}`}</code> pairs, ASCII letters, digits, <code>_</code> and <code>-</code>.</td></tr>
            <tr><td><code>attachments</code></td><td>Up to 10, as base64 <code>content</code> with <code>filename</code>, optional <code>content_type</code> and <code>content_id</code> for inline images. <code>path</code> (remote URL) is not supported. Executable types are refused.</td></tr>
          </tbody>
        </ScrollTable>
        <p>The total of HTML, text and decoded attachments is limited to 10 MB. Unknown fields return <code>422</code> instead of being silently ignored.</p>
        <h2>Recipients and quota</h2>
        <p>Quota is counted per unique delivery recipient. If the same address appears in several fields it counts once, with priority To → Cc → Bcc. Suppressed addresses are accepted for visibility but neither sent nor charged.</p>
      </>
    ),
  },
  {
    slug: "test-mode",
    title: "Test mode",
    body: (
      <>
        <p>Test keys capture email in the project&apos;s test inbox. Whether a key is test or live is read from the key record in the database — the <code>hv_test_</code> prefix is only a visual hint. A database rule also refuses send jobs for test emails, so test mail cannot reach the sending provider.</p>
    <DocFigure name="first-email" alt="Send with a test key and read the captured result in the test inbox." caption="Test-mode messages are captured inside the project." />
        <ul>
          <li>Separate limits: 500 test emails per project per day and 50 MB stored by default. They never use live quota.</li>
          <li>Kept for 7 days, then deleted. You can delete them at any time.</li>
          <li>HTML is shown in a sandboxed frame without scripts. Remote images load only after you choose to, and links are listed instead of opened.</li>
        </ul>
        <p>A test inbox shows what you sent. It does not prove inbox placement at Gmail or Outlook.</p>
      </>
    ),
  },
  {
    slug: "domains",
    title: "Domains and DNS",
    body: (
      <>
        <p>Hedvi uses Amazon SES Easy DKIM and a custom MAIL FROM subdomain (default <code>send.yourdomain</code>). You publish:</p>
        <DocFigure name="domain-setup" alt="Add a domain, copy records, publish DNS and verify." caption="Cloudflare users can follow the dedicated DNS walkthrough in this documentation." />
        <ul>
          <li>A <strong>TXT ownership challenge</strong> at <code>_hedvi-verification.yourdomain</code>, with the exact current claim value shown in Hedvi. A newly added domain gets a new value; old DKIM records alone cannot verify it.</li>
          <li>Three <strong>DKIM CNAME</strong> records — these sign your mail for your domain.</li>
          <li>An <strong>MX</strong> and a <strong>TXT (SPF)</strong> record on the MAIL FROM subdomain only.</li>
        </ul>
        <p><strong>Your existing SPF and DMARC records stay as they are.</strong> Hedvi reads them and explains what it found, but never asks you to replace them: SPF is handled on the subdomain, and DKIM aligns with your domain so DMARC passes. If you have no DMARC record we suggest a safe start (<code>p=none</code>).</p>
        <p>We detect your DNS provider from its nameservers as a hint (Cloudflare, Loopia, One.com and others) and show provider tips. For Cloudflare, set DKIM records to <em>DNS only</em>.</p>
        <p>Checks run automatically with backoff (30 s, 1 min, 2 min … hourly) and show the last check time. After 72 hours without success the domain is marked failed until you press <em>Check now</em>.</p>
      </>
    ),
  },
  {
    slug: "statuses",
    title: "Delivery statuses",
    body: (
      <>
        <p>Status is tracked per recipient. One email can be delivered to one address and bounce for another.</p>
    <DocFigure name="email-lifecycle" alt="Follow a recorded email from queue to provider and recipient mail server." caption="The email log distinguishes a queued send from provider and recipient-server acceptance." />
        <ScrollTable>
          <thead><tr><th>Status</th><th>Meaning</th></tr></thead>
          <tbody>
            <tr><td><code>queued</code></td><td>Durably accepted by Hedvi, waiting to be handed to the sending provider.</td></tr>
            <tr><td><code>sent</code></td><td>Amazon SES accepted the message.</td></tr>
            <tr><td><code>delivered</code></td><td>The recipient&apos;s mail server accepted it. This is not a guarantee of inbox placement.</td></tr>
            <tr><td><code>delivery_delayed</code></td><td>Temporary problem; SES keeps trying.</td></tr>
            <tr><td><code>bounced</code></td><td>Permanent or final failure. Permanent bounces suppress the address for 90 days in your organization.</td></tr>
            <tr><td><code>complained</code></td><td>The recipient reported spam. The address is suppressed in your organization.</td></tr>
            <tr><td><code>suppressed</code></td><td>Not sent because the address is on your organization&apos;s suppression list. Not charged.</td></tr>
            <tr><td><code>failed</code></td><td>Not sent (for example rejected by the provider). Quota is released.</td></tr>
            <tr><td><code>delivery_unknown</code></td><td>The provider&apos;s answer was ambiguous (for example a timeout after the request was sent). Hedvi does <strong>not</strong> retry automatically, because that could create a duplicate. It reconciles from provider events and alerts the operator.</td></tr>
            <tr><td><code>test_captured</code></td><td>Hedvi extension: a test email captured in the test inbox.</td></tr>
          </tbody>
        </ScrollTable>
        <p>Late or duplicate events never overwrite a more significant status: a delayed <code>delivered</code> event cannot replace a known bounce.</p>
        <h3>Resending</h3>
        <p>Resending an email creates a new email, uses quota again and can produce a duplicate for the recipient. Resending a webhook re-delivers the same event with the same id.</p>
      </>
    ),
  },
  {
    slug: "idempotency",
    title: "Idempotency",
    body: (
      <>
        <p>Send an <code>Idempotency-Key</code> header (1–256 printable characters) to make retries safe. Keys are scoped to your organization, project, mode (test/live) and operation and kept for 24 hours. If an error leaves acceptance uncertain, reuse the original key and identical payload within that lifetime; otherwise inspect the email log before resending.</p>
    <DocFigure name="retry-request" alt="Retry one operation with the same body and idempotency key." caption="A retry can recover the existing email ID." />
        <ul>
          <li>Same key and same body: the original response is returned with <code>Idempotent-Replayed: true</code>.</li>
          <li>Same key, different body: <code>409 invalid_idempotent_request</code>.</li>
          <li>Concurrent requests with the same key create one email.</li>
        </ul>
        <p>Without a key, a retry after a lost response can create a second email.</p>
      </>
    ),
  },
  {
    slug: "webhooks",
    title: "Webhooks",
    body: (
      <>
        <p>Add endpoints under <strong>Developers → Webhooks</strong>. Hedvi sends Resend-compatible payloads (<code>type</code>, <code>created_at</code>, <code>data</code>) for <code>email.sent</code>, <code>email.delivered</code>, <code>email.delivery_delayed</code>, <code>email.bounced</code>, <code>email.complained</code>, <code>email.failed</code> and <code>email.suppressed</code>, plus opt-in Hedvi events (<code>hedvi.email.delivery_unknown</code>, <code>hedvi.quota.warning</code>, <code>hedvi.project.paused</code>, <code>domain.updated</code>). Recipient events contain the affected recipient in <code>to</code>.</p>
    <DocFigure name="webhook-delivery" alt="Verify the signature, deduplicate the event and acknowledge its receipt." caption="Verify each delivery before processing the event." />
        <h2>Verify signatures</h2>
        <p>Requests are signed with the <a href="https://www.standardwebhooks.com/">Standard Webhooks</a> scheme over <code>id.timestamp.body</code> (headers <code>webhook-id</code>, <code>webhook-timestamp</code>, <code>webhook-signature</code>, mirrored as <code>svix-*</code>). Reject timestamps older than 5 minutes. The Resend SDK verifies them:</p>
        <CodeBlock code={verifyNode} />
        <h2>Delivery</h2>
        <ul>
          <li>At least once. The event id is stable across retries and manual resends — deduplicate on it.</li>
          <li>Any 2xx within 15 seconds counts as success. Retries after 5 s, 5 min, 30 min, 2 h, 5 h, 10 h and 10 h; then the delivery is marked failed and can be resent from the dashboard.</li>
          <li>Redirects are not followed. Endpoints must be public HTTPS; private, loopback and cloud metadata addresses are refused, also after DNS resolution.</li>
          <li>Only the first 1 KB of your response is stored, for troubleshooting.</li>
        </ul>
      </>
    ),
  },
  {
    slug: "errors",
    title: "Errors and limits",
    body: (
      <>
        <p>Errors use Resend&apos;s shape: <code>{`{"statusCode": 422, "name": "validation_error", "message": "…"}`}</code>.</p>
    <DocFigure name="retry-request" alt="Inspect the error and reuse the original idempotency key for a safe retry." caption="Correct invalid requests and preserve the key for an uncertain accepted send." />
        <ScrollTable>
          <thead><tr><th>Status</th><th>Name</th><th>When</th></tr></thead>
          <tbody>
            <tr><td>401</td><td><code>missing_api_key</code></td><td>No <code>Authorization: Bearer</code> header.</td></tr>
            <tr><td>403</td><td><code>invalid_api_key</code></td><td>Unknown or revoked key.</td></tr>
            <tr><td>401</td><td><code>restricted_api_key</code></td><td>Sending-only key used for another endpoint.</td></tr>
            <tr><td>403</td><td><code>validation_error</code></td><td>Domain not verified or live sending not approved.</td></tr>
            <tr><td>409</td><td><code>invalid_idempotent_request</code></td><td>Idempotency key reused with another body.</td></tr>
            <tr><td>422</td><td><code>validation_error</code> · <code>missing_required_field</code> · <code>invalid_attachment</code></td><td>Invalid or unsupported request.</td></tr>
            <tr><td>429</td><td><code>rate_limit_exceeded</code></td><td>Too many requests (10/s shared by organization keys), a warm-up limit or an operational resource limit.</td></tr>
            <tr><td>429</td><td><code>daily_quota_exceeded</code> · <code>monthly_quota_exceeded</code></td><td>Daily limit or quota reached. Hard stop: nothing was sent or charged.</td></tr>
          </tbody>
        </ScrollTable>
        <p>New live projects start at 25 recipients per hour and 100 per day. Limits are raised after review, not automatically.</p>
        <p>Operational hourly/daily limits count all unique requested recipients, including suppressed addresses. Suppressed recipients do not consume paid sending credits. A separate 1 GiB retained live-payload budget per project protects storage; it is released when content is deleted and is separate from plan attachment allowances.</p>
      </>
    ),
  },
  {
    slug: "compatibility",
    title: "Resend compatibility",
    body: (
      <>
        <p>Tested against <strong>resend-node 6.39.0</strong> and <strong>resend-python 2.54.0</strong>. Other SDKs and versions are not promised until tested.</p>
    <DocFigure name="api-request" alt="Set the Hedvi API base URL and project key, then inspect the response." caption="Use a test project to verify the features your integration needs." />
        <ScrollTable>
          <thead><tr><th>Endpoint / feature</th><th>Status</th></tr></thead>
          <tbody>
            <tr><td><code>POST /emails</code></td><td>Supported (fields above). <code>scheduled_at</code>, <code>template</code>, <code>topic_id</code> and attachment <code>path</code> return 422.</td></tr>
            <tr><td><code>GET /emails/{"{id}"}</code>, <code>GET /emails</code></td><td>Supported. <code>last_event</code> may also be <code>delivery_unknown</code> or <code>test_captured</code>.</td></tr>
            <tr><td><code>POST/GET/DELETE /domains</code>, <code>POST /domains/{"{id}"}/verify</code></td><td>Supported. Region is always <code>eu-north-1</code>; tracking and receiving are refused.</td></tr>
            <tr><td><code>Idempotency-Key</code>, error format, rate limit headers</td><td>Supported.</td></tr>
            <tr><td>Webhook signatures</td><td>Compatible with <code>resend.webhooks.verify</code>.</td></tr>
            <tr><td>Batch, scheduling, cancel, API key API, audiences, contacts, broadcasts, templates, receiving, open/click tracking</td><td>Not supported yet — these return an explicit 404 or 422.</td></tr>
          </tbody>
        </ScrollTable>
        <p>Hedvi extensions live under <code>/hedvi/v1</code>: <code>GET /hedvi/v1/emails/{"{id}"}</code> (recipients, events, attempts) and <code>GET /hedvi/v1/usage</code>.</p>
      </>
    ),
  },
  {
    slug: "switch-from-resend",
    title: "Switch from Resend",
    body: (
      <>
        <p>Changing the client takes a minute; switching production safely takes verified prerequisites. We support the switch with a checklist, not a promise of zero work.</p>
    <DocFigure name="migration-canary" alt="Set up a verified domain, move one workflow and evaluate outcomes before expanding." caption="Keep the previous provider available during a controlled migration." />
        <h2>Client change</h2>
        <CodeBlock code={`# Node (resend 6.39.0)
RESEND_BASE_URL=${API}
# Python (resend 2.54.0)
RESEND_API_URL=${API}
# Both: use a Hedvi key instead of the re_ key
RESEND_API_KEY=hv_live_…`} />
        <h2>Before you switch</h2>
        <ul className="checks">
          <li>Domain added in Hedvi and current ownership TXT, DKIM and MAIL FROM verified (DNS can take time).</li>
          <li>Live sending approved for the project.</li>
          <li>Suppressions exported from Resend and added in Hedvi.</li>
          <li>Webhook endpoints updated with the Hedvi signing secret.</li>
          <li>Your code uses only supported endpoints and fields (see <Link href="/docs/compatibility">compatibility</Link>).</li>
          <li>Attachment sizes and recipient counts within Hedvi&apos;s limits.</li>
        </ul>
        <h2>Canary and rollback</h2>
        <p>Route a small share of low-risk mail to Hedvi first (for example by environment variable per job type), compare outcomes, then increase. Keep the Resend configuration so rollback is a config change.</p>
      </>
    ),
  },
  {
    slug: "data-and-retention",
    title: "Data and retention",
    body: (
      <>
    <DocFigure name="content-retention" alt="Content is retained for processing and expires according to its configured schedule." caption="Expiry removes email preview and resend access." />
        <p className="notice notice-warn">Read the <Link href="/legal/privacy">Privacy Notice</Link>, <Link href="/legal/dpa">DPA</Link> and <Link href="/legal/subprocessors">service provider list</Link> for the published service documents.</p>
        <ScrollTable>
          <thead><tr><th>Data</th><th>Default retention</th></tr></thead>
          <tbody>
            <tr><td>Email content (email-record subject, HTML, text, attachments)</td><td>7 days (shorter on request; 0 = deleted after processing). S3 payloads are encrypted. Subject copies in webhook events follow the separate metadata retention.</td></tr>
            <tr><td>Event metadata (recipients, statuses, timestamps)</td><td>Free 14 days · Flex 30 days · Pro 90 days.</td></tr>
            <tr><td>Test inbox</td><td>7 days.</td></tr>
            <tr><td>Idempotency records</td><td>24 hours.</td></tr>
            <tr><td>Webhook responses</td><td>First 1 KB, kept with metadata.</td></tr>
          </tbody>
        </ScrollTable>
        <p>Content is kept while an email is queued or retrying, then deleted when it expires. Webhook events also contain the subject, sender and recipient addresses; these event copies remain for the organization’s metadata retention (14, 30 or 90 days), even when email-content retention is 0. Your webhook receiver has its own retention policy. After email-content deletion, resend and content search are impossible.</p>
        <p>Email payload storage and SES processing use AWS Stockholm (eu-north-1). The application, database and its backups use Railway in Amsterdam. Recipient mail servers operate where their providers host them. Provider administration and support may involve processing outside these regions; see the subprocessor list.</p>
      </>
    ),
  },
  ...guides,
];

export function findDoc(slug: string) {
  return docs.find((d) => d.slug === slug);
}
