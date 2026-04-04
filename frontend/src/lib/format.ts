const USDC_DECIMALS = 6;

export function formatUSDC(baseUnits: string): string {
  const n = BigInt(baseUnits);
  const whole = n / BigInt(10 ** USDC_DECIMALS);
  const frac = n % BigInt(10 ** USDC_DECIMALS);
  const fracStr = frac.toString().padStart(USDC_DECIMALS, "0").replace(/0+$/, "");
  return fracStr ? `${whole}.${fracStr}` : whole.toString();
}

export function parseUSDCToBaseUnits(human: string): string {
  const parts = human.split(".");
  const whole = parts[0] ?? "0";
  const frac = (parts[1] ?? "").padEnd(USDC_DECIMALS, "0").slice(0, USDC_DECIMALS);
  return (BigInt(whole) * BigInt(10 ** USDC_DECIMALS) + BigInt(frac)).toString();
}

export function formatNativeBalance(wei: string): string {
  if (!wei || wei === "0") return "0";
  const n = BigInt(wei);
  const whole = n / 10n ** 18n;
  const frac = n % 10n ** 18n;
  const fracStr = frac.toString().padStart(18, "0").slice(0, 4).replace(/0+$/, "");
  return fracStr ? `${whole}.${fracStr}` : whole.toString();
}

export function shortenAddress(addr: string): string {
  return `${addr.slice(0, 6)}...${addr.slice(-4)}`;
}
