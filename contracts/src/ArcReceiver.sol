// SPDX-License-Identifier: MIT
pragma solidity ^0.8.20;

/// @title ArcReceiver
/// @notice Deployed on Arc. Self-relays a CCTP message (receiveMessage + mint),
///         then deposits the minted USDC into Gateway Wallet in a single tx.
/// @dev Only the operator (shop backend) can call relayAndDeposit().
contract ArcReceiver {
    IERC20 public immutable usdc;
    address public immutable gatewayWallet;
    address public immutable operator;
    IMessageTransmitter public immutable messageTransmitter;

    event Deposited(address indexed depositor, uint256 amount);

    constructor(
        address _usdc,
        address _gatewayWallet,
        address _operator,
        address _messageTransmitter
    ) {
        usdc = IERC20(_usdc);
        gatewayWallet = _gatewayWallet;
        operator = _operator;
        messageTransmitter = IMessageTransmitter(_messageTransmitter);
    }

    /// @notice Relay a CCTP message to mint USDC here, then deposit into Gateway.
    /// @param message     Raw CCTP message bytes (from attestation API)
    /// @param attestation Circle attestation signature (from attestation API)
    /// @param depositor   Address to credit in Gateway's ledger (shop wallet)
    function relayAndDeposit(
        bytes calldata message,
        bytes calldata attestation,
        address depositor
    ) external {
        require(msg.sender == operator, "only operator");

        // 1. Relay CCTP message — mints USDC to this contract (mintRecipient).
        bool success = messageTransmitter.receiveMessage(message, attestation);
        require(success, "receiveMessage failed");

        // 2. Deposit all received USDC into Gateway on behalf of depositor.
        uint256 balance = usdc.balanceOf(address(this));
        require(balance > 0, "no USDC received");

        usdc.approve(gatewayWallet, balance);
        IGatewayWallet(gatewayWallet).depositFor(address(usdc), depositor, balance);

        emit Deposited(depositor, balance);
    }
}

interface IERC20 {
    function balanceOf(address account) external view returns (uint256);
    function approve(address spender, uint256 amount) external returns (bool);
}

interface IGatewayWallet {
    function depositFor(address token, address depositor, uint256 value) external;
}

interface IMessageTransmitter {
    function receiveMessage(bytes calldata message, bytes calldata attestation) external returns (bool);
}
