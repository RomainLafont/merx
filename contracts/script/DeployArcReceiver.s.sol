// SPDX-License-Identifier: MIT
pragma solidity ^0.8.20;

import {Script, console} from "forge-std/Script.sol";
import {ArcReceiver} from "../src/ArcReceiver.sol";

/// @notice Deploy ArcReceiver on Arc testnet.
///
///   PRIVATE_KEY=0x... forge script script/DeployArcReceiver.s.sol \
///     --rpc-url https://rpc.testnet.arc.network --broadcast --skip-simulation
contract DeployArcReceiver is Script {
    address constant GATEWAY_WALLET = 0x0077777d7EBA4688BDeF3E311b846F25870A19B9;
    address constant MESSAGE_TRANSMITTER = 0xE737e5cEBEEBa77EFE34D4aa090756590b1CE275;
    address constant USDC = 0x3600000000000000000000000000000000000000;

    function run() external {
        uint256 deployerKey = vm.envUint("PRIVATE_KEY");
        address operator = vm.addr(deployerKey);

        vm.startBroadcast(deployerKey);
        ArcReceiver receiver = new ArcReceiver(USDC, GATEWAY_WALLET, operator, MESSAGE_TRANSMITTER);
        vm.stopBroadcast();

        console.log("ArcReceiver deployed at:", address(receiver));
        console.log("  USDC:", USDC);
        console.log("  Gateway Wallet:", GATEWAY_WALLET);
        console.log("  MessageTransmitter:", MESSAGE_TRANSMITTER);
        console.log("  Operator:", operator);
    }
}
