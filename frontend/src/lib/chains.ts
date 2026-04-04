import { type Chain, defineChain } from "viem";
import { sepolia, baseSepolia, optimismSepolia, arbitrumSepolia, avalancheFuji, polygonAmoy } from "viem/chains";

export const unichainSepolia = defineChain({
  id: 1301,
  name: "Unichain Sepolia",
  nativeCurrency: { name: "Ether", symbol: "ETH", decimals: 18 },
  rpcUrls: { default: { http: ["https://sepolia.unichain.org"] } },
  testnet: true,
});

export const sonicBlaze = defineChain({
  id: 57054,
  name: "Sonic Blaze",
  nativeCurrency: { name: "Sonic", symbol: "S", decimals: 18 },
  rpcUrls: { default: { http: ["https://rpc.blaze.soniclabs.com"] } },
  testnet: true,
});

export const worldchainSepolia = defineChain({
  id: 4801,
  name: "Worldchain Sepolia",
  nativeCurrency: { name: "Ether", symbol: "ETH", decimals: 18 },
  rpcUrls: { default: { http: ["https://worldchain-sepolia.g.alchemy.com/public"] } },
  testnet: true,
});

export const seiAtlantic = defineChain({
  id: 1328,
  name: "Sei Atlantic",
  nativeCurrency: { name: "Sei", symbol: "SEI", decimals: 18 },
  rpcUrls: { default: { http: ["https://evm-rpc-testnet.sei-apis.com"] } },
  testnet: true,
});

export const supportedChains: readonly [Chain, ...Chain[]] = [
  sepolia,
  avalancheFuji,
  optimismSepolia,
  arbitrumSepolia,
  baseSepolia,
  polygonAmoy,
  unichainSepolia,
  sonicBlaze,
  worldchainSepolia,
  seiAtlantic,
];

export function chainName(chainId: number): string {
  const chain = supportedChains.find((c) => c.id === chainId);
  return chain?.name ?? `Chain ${chainId}`;
}

const explorerURLs: Record<number, string> = {
  11155111: "https://sepolia.etherscan.io",
  84532:    "https://sepolia.basescan.org",
  1301:     "https://sepolia.uniscan.xyz",
  5042002:  "https://testnet.arcscan.app",
};

export function txExplorerURL(chainId: number, txHash: string): string | null {
  const base = explorerURLs[chainId];
  return base ? `${base}/tx/${txHash}` : null;
}

export function arcTxURL(txHash: string): string {
  return `https://testnet.arcscan.app/tx/${txHash}`;
}
