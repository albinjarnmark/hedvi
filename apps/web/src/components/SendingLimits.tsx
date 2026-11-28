export type SendingLimitsData = {
  hourly_limit: number;
  daily_limit: number;
  org_hourly_limit: number;
  org_daily_limit: number;
};

export function sendingWindowLabel(hourly: number, daily: number) {
  if (hourly < 0 || daily < 0) return "Sending stopped by an operator restriction";
  if (hourly === 0 && daily === 0) return "No hourly or daily plan limit";
  return [hourly > 0 ? `${hourly.toLocaleString()} recipients/hour` : null, daily > 0 ? `${daily.toLocaleString()}/day` : null].filter(Boolean).join(" · ");
}

export function SendingLimits({ limits }: { limits: SendingLimitsData }) {
  return <div>
    <p>{sendingWindowLabel(limits.hourly_limit, limits.daily_limit)}.</p>
    {(limits.org_hourly_limit > 0 || limits.org_daily_limit > 0) && <p className="small muted">Shared across your organization: {sendingWindowLabel(limits.org_hourly_limit, limits.org_daily_limit)}.</p>}
    <p className="small muted">Available recipients and attachment data still apply. Traffic peaks are queued for delivery. API rate limits and abuse protections remain active.</p>
  </div>;
}
