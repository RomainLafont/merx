// Map chainId to local icon path in /public/chains/
const icons: Record<number, string> = {
  11155111: "/chains/ethereum.png",  // Ethereum Sepolia
  43113:    "/chains/avalanche.png", // Avalanche Fuji
  11155420: "/chains/optimism.png",  // OP Sepolia
  421614:   "/chains/arbitrum.png",  // Arbitrum Sepolia
  84532:    "/chains/base.png",      // Base Sepolia
  80002:    "/chains/polygon.png",   // Polygon Amoy
  1301:     "/chains/unichain.png",  // Unichain Sepolia
  57054:    "/chains/sonic.png",     // Sonic Blaze
  4801:     "/chains/worldchain.png",// Worldchain Sepolia
  1328:     "/chains/sei.png",       // Sei Atlantic
};

export function chainIcon(chainId: number): string | undefined {
  return icons[chainId];
}
