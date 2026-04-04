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

export const lineaSepolia = defineChain({
  id: 59141,
  name: "Linea Sepolia",
  nativeCurrency: { name: "Ether", symbol: "ETH", decimals: 18 },
  rpcUrls: { default: { http: ["https://rpc.sepolia.linea.build"] } },
  testnet: true,
});

export const codexTestnet = defineChain({
  id: 812242,
  name: "Codex Testnet",
  nativeCurrency: { name: "Ether", symbol: "ETH", decimals: 18 },
  rpcUrls: { default: { http: ["https://rpc.codex-stg.xyz"] } },
  testnet: true,
});

export const monadTestnet = defineChain({
  id: 10143,
  name: "Monad Testnet",
  nativeCurrency: { name: "Monad", symbol: "MON", decimals: 18 },
  rpcUrls: { default: { http: ["https://testnet-rpc.monad.xyz"] } },
  testnet: true,
});

export const hyperEvmTestnet = defineChain({
  id: 998,
  name: "HyperEVM Testnet",
  nativeCurrency: { name: "Ether", symbol: "ETH", decimals: 18 },
  rpcUrls: { default: { http: ["https://rpc.hyperliquid-testnet.xyz/evm"] } },
  testnet: true,
});

export const inkSepolia = defineChain({
  id: 763373,
  name: "Ink Sepolia",
  nativeCurrency: { name: "Ether", symbol: "ETH", decimals: 18 },
  rpcUrls: { default: { http: ["https://rpc-gel-sepolia.inkonchain.com"] } },
  testnet: true,
});

export const plumeTestnet = defineChain({
  id: 98867,
  name: "Plume Testnet",
  nativeCurrency: { name: "Ether", symbol: "ETH", decimals: 18 },
  rpcUrls: { default: { http: ["https://testnet-rpc.plume.org"] } },
  testnet: true,
});

export const edgeTestnet = defineChain({
  id: 33431,
  name: "EDGE Testnet",
  nativeCurrency: { name: "Ether", symbol: "ETH", decimals: 18 },
  rpcUrls: { default: { http: ["https://edge-testnet.g.alchemy.com/public"] } },
  testnet: true,
});

export const morphHoodi = defineChain({
  id: 2910,
  name: "Morph Hoodi",
  nativeCurrency: { name: "Ether", symbol: "ETH", decimals: 18 },
  rpcUrls: { default: { http: ["https://rpc-hoodi.morphl2.io"] } },
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
  lineaSepolia,
  codexTestnet,
  monadTestnet,
  hyperEvmTestnet,
  inkSepolia,
  plumeTestnet,
  edgeTestnet,
  morphHoodi,
];

export function chainName(chainId: number): string {
  const chain = supportedChains.find((c) => c.id === chainId);
  return chain?.name ?? `Chain ${chainId}`;
}

const explorerURLs: Record<number, string> = {
  11155111: "https://sepolia.etherscan.io",
  43113:    "https://testnet.snowtrace.io",
  11155420: "https://sepolia-optimism.etherscan.io",
  421614:   "https://sepolia.arbiscan.io",
  84532:    "https://sepolia.basescan.org",
  80002:    "https://amoy.polygonscan.com",
  1301:     "https://sepolia.uniscan.xyz",
  57054:    "https://blaze.soniclabs.com",
  4801:     "https://sepolia.worldscan.org",
  1328:     "https://seistream.app",
  59141:    "https://sepolia.lineascan.build",
  812242:   "https://explorer.codex-stg.xyz",
  10143:    "https://testnet.monadexplorer.com",
  998:      "https://testnet.purrsec.com",
  763373:   "https://explorer-sepolia.inkonchain.com",
  98867:    "https://testnet-explorer.plume.org",
  33431:    "https://edge-testnet.explorer.alchemy.com",
  2910:     "https://explorer-hoodi.morphl2.io",
  5042002:  "https://testnet.arcscan.app",
};

export function txExplorerURL(chainId: number, txHash: string): string | null {
  const base = explorerURLs[chainId];
  return base ? `${base}/tx/${txHash}` : null;
}

export function arcTxURL(txHash: string): string {
  return `https://testnet.arcscan.app/tx/${txHash}`;
}
