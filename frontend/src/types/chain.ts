export interface TokenEntry {
  symbol: string;
  decimals: number;
  address: string;
}

export interface ChainInfo {
  name: string;
  chainId: number;
  cctpDomain: number;
  explorer: string;
  uniswapSupported: boolean;
  tokens: TokenEntry[];
}
