// SPDX-License-Identifier: MIT
pragma solidity ^0.8.20;

import {Script, console} from "forge-std/Script.sol";
import {ShopPaymaster} from "../src/ShopPaymaster.sol";

/// @notice Deploy ShopPaymaster on a source chain.
///
///   forge script script/DeployShopPaymaster.s.sol \
///     --rpc-url https://sepolia.unichain.org --broadcast
///
/// Environment:
///   PRIVATE_KEY — deployer key
///   USDC        — (optional) override USDC address
///   TOKEN_MESSENGER — (optional) override TokenMessengerV2 address
contract DeployShopPaymaster is Script {
    // Defaults (testnets).
    address constant TOKEN_MESSENGER_V2 = 0x8FE6B999Dc680CcFDD5Bf7EB0974218be2542DAA;
    // mintRecipient = ArcDepositor on Arc (receives USDC from CCTP, then flushes to Gateway).
    // mintRecipient = ArcReceiver on Arc (receives CCTP mint, then deposits to Gateway).
    address constant ARC_RECEIVER = 0x0A4eFeFbB7286D864cDDf6957642b2B11cd58f30;
    uint32 constant ARC_DOMAIN = 26;

    // USDC by chain ID.
    function _usdc() internal view returns (address) {
        address env = vm.envOr("USDC", address(0));
        if (env != address(0)) return env;
        uint256 chainId = block.chainid;
        if (chainId == 1301) return 0x31d0220469e10c4E71834a79b1f276d740d3768F;   // Unichain Sepolia
        if (chainId == 84532) return 0x036CbD53842c5426634e7929541eC2318f3dCF7e;  // Base Sepolia
        if (chainId == 11155111) return 0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238; // Ethereum Sepolia
        revert("unsupported chain - set USDC env var");
    }

    function run() external {
        uint256 deployerKey = vm.envUint("PRIVATE_KEY");
        address usdc = _usdc();
        address messenger = vm.envOr("TOKEN_MESSENGER", TOKEN_MESSENGER_V2);

        vm.startBroadcast(deployerKey);
        ShopPaymaster paymaster = new ShopPaymaster(usdc, messenger, ARC_DOMAIN, ARC_RECEIVER);
        vm.stopBroadcast();

        console.log("ShopPaymaster deployed at:", address(paymaster));
        console.log("  USDC:", usdc);
        console.log("  TokenMessenger:", messenger);
        console.log("  Arc domain:", ARC_DOMAIN);
        console.log("  Arc receiver (mintRecipient):", ARC_RECEIVER);
    }
}
