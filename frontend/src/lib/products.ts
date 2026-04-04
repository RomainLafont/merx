export interface Product {
  id: string;
  title: string;
  author: string;
  price: string; // human-readable USDC e.g. "12.99"
  cover: string; // emoji placeholder
  description: string;
}

export const products: Product[] = [
  {
    id: "mastering-solidity",
    title: "Mastering Solidity",
    author: "Adrian Hetman",
    price: "29.99",
    cover: "\u{1F4D8}",
    description:
      "A comprehensive guide to smart contract development on Ethereum, from basics to advanced patterns.",
  },
  {
    id: "defi-handbook",
    title: "The DeFi Handbook",
    author: "Sarah Chen",
    price: "19.99",
    cover: "\u{1F4D7}",
    description:
      "Everything you need to know about decentralized finance: AMMs, lending protocols, and yield strategies.",
  },
  {
    id: "zero-knowledge",
    title: "Zero Knowledge Proofs",
    author: "Eli Ben-Sasson",
    price: "49.99",
    cover: "\u{1F4D5}",
    description:
      "Deep dive into ZK-SNARKs, ZK-STARKs, and their applications in blockchain privacy and scaling.",
  },
  {
    id: "crypto-economics",
    title: "Cryptoeconomics 101",
    author: "Vitalik B.",
    price: "14.99",
    cover: "\u{1F4D9}",
    description:
      "An introduction to mechanism design, tokenomics, and incentive structures in crypto networks.",
  },
  {
    id: "nft-art",
    title: "NFTs & Digital Art",
    author: "Pak & Friends",
    price: "0.99",
    cover: "\u{1F3A8}",
    description:
      "Explore the intersection of art and blockchain: minting, marketplaces, and the creator economy.",
  },
  {
    id: "web3-security",
    title: "Web3 Security Auditing",
    author: "samczsun",
    price: "39.99",
    cover: "\u{1F6E1}\u{FE0F}",
    description:
      "Learn to find and exploit smart contract vulnerabilities. Real-world case studies from major hacks.",
  },
];

export function getProduct(id: string): Product | undefined {
  return products.find((p) => p.id === id);
}
