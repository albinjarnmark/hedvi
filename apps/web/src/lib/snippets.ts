export type Snippet = { label: string; code: string };

export function sendSnippets(apiUrl: string, key: string, from: string, to: string): Snippet[] {
  return [
    {
      label: "curl",
      code: `curl -X POST ${apiUrl}/emails \\
  -H "Authorization: Bearer ${key}" \\
  -H "Content-Type: application/json" \\
  -H "Idempotency-Key: welcome-42" \\
  -d '{
    "from": "${from}",
    "to": "${to}",
    "subject": "Hello from Hedvi",
    "html": "<p>It works.</p>"
  }'`,
    },
    {
      label: "Node.js",
      code: `// npm install resend@6.39.0
// RESEND_BASE_URL=${apiUrl}
import { Resend } from "resend";

const resend = new Resend("${key}");
const { data, error } = await resend.emails.send(
  { from: "${from}", to: "${to}", subject: "Hello from Hedvi", html: "<p>It works.</p>" },
  { idempotencyKey: "welcome-42" },
);`,
    },
    {
      label: "Python",
      code: `# pip install resend==2.54.0
# RESEND_API_URL=${apiUrl}
import resend

resend.api_key = "${key}"
email = resend.Emails.send({
    "from": "${from}",
    "to": "${to}",
    "subject": "Hello from Hedvi",
    "html": "<p>It works.</p>",
})`,
    },
  ];
}
