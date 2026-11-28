import { CopyButton } from "./CopyButton";
import { agentSetupPrompt } from "@/lib/agent-setup";

export function AgentSetup() {
  return <section className="card stack" style={{ marginTop: 20 }}>
    <div className="row between"><div><span className="eyebrow">Bring your coding agent</span><h2>Get started with Hedvi</h2></div><CopyButton value={agentSetupPrompt} label="Copy setup prompt" /></div>
    <p className="small muted">Paste this prompt into the coding agent you already use. It will help you get a Hedvi account, connect your project, and send your first test email.</p>
    <p className="small muted">You stay in control of signup, secrets and going live. The prompt contains no account details or API keys.</p>
    <details><summary>Read the prompt</summary><pre className="code agent-prompt">{agentSetupPrompt}</pre></details>
  </section>;
}
