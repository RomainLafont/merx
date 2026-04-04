import { http, createConfig, fallback } from "wagmi";
import { injected } from "wagmi/connectors";
import { supportedChains } from "./chains";

// Build transports: use the chain's default RPC with a fallback
const transports: Record<number, ReturnType<typeof http>> = {};
for (const chain of supportedChains) {
  const url = chain.rpcUrls.default.http[0];
  transports[chain.id] = url ? http(url) : http();
}

export const config = createConfig({
  chains: supportedChains,
  connectors: [injected()],
  transports,
});
