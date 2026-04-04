// SPDX-License-Identifier: MIT
pragma solidity ^0.8.20;

/// @title ShopPaymaster
/// @notice Accepts a USDC EIP-2612 permit and bridges to Arc via CCTP V2 Forwarding Service in one tx.
/// @dev Deploy one per source chain. Immutable — no owner, no upgradability.
contract ShopPaymaster {
    IERC20Permit public immutable usdc;
    ITokenMessenger public immutable tokenMessenger;
    uint32 public immutable arcDomain;
    bytes32 public immutable mintRecipient; // ArcDepositor on Arc, left-padded

    event Payment(address indexed from, uint256 amount);

    constructor(
        address _usdc,
        address _tokenMessenger,
        uint32 _arcDomain,
        address _gatewayWallet
    ) {
        usdc = IERC20Permit(_usdc);
        tokenMessenger = ITokenMessenger(_tokenMessenger);
        arcDomain = _arcDomain;
        mintRecipient = bytes32(uint256(uint160(_gatewayWallet)));
    }

    /// @notice Pay with a pre-signed EIP-2612 permit. The backend broadcasts this tx on behalf of the customer.
    /// @param owner       Customer address (permit signer)
    /// @param amount      USDC amount (6 decimals)
    /// @param deadline    Permit deadline
    /// @param v           Permit signature v
    /// @param r           Permit signature r
    /// @param s           Permit signature s
    /// @param maxFee      Max fee for CCTP forwarding service
    function payWithPermit(
        address owner,
        uint256 amount,
        uint256 deadline,
        uint8 v,
        bytes32 r,
        bytes32 s,
        uint256 maxFee
    ) external {
        // 1. Execute permit: owner approves this contract.
        usdc.permit(owner, address(this), amount, deadline, v, r, s);

        // 2. Pull USDC from the customer.
        require(usdc.transferFrom(owner, address(this), amount), "transferFrom failed");

        // 3. Approve TokenMessenger to spend.
        require(usdc.approve(address(tokenMessenger), amount), "approve failed");

        // 4. Bridge to Arc via CCTP V2 Fast Transfer (self-relay).
        tokenMessenger.depositForBurn(
            amount,
            arcDomain,
            mintRecipient,
            address(usdc),
            bytes32(0), // destinationCaller: permissionless
            maxFee,
            0  // minFinalityThreshold: 0 for fast transfer
        );

        emit Payment(owner, amount);
    }
}

interface IERC20Permit {
    function permit(
        address owner,
        address spender,
        uint256 value,
        uint256 deadline,
        uint8 v,
        bytes32 r,
        bytes32 s
    ) external;

    function transferFrom(address from, address to, uint256 amount) external returns (bool);
    function approve(address spender, uint256 amount) external returns (bool);
    function allowance(address owner, address spender) external view returns (uint256);
    function balanceOf(address account) external view returns (uint256);
    function nonces(address owner) external view returns (uint256);
    function DOMAIN_SEPARATOR() external view returns (bytes32);
}

interface ITokenMessenger {
    function depositForBurn(
        uint256 amount,
        uint32 destinationDomain,
        bytes32 mintRecipient,
        address burnToken,
        bytes32 destinationCaller,
        uint256 maxFee,
        uint32 minFinalityThreshold
    ) external;
}
