namespace MailCat;

/// <summary>Invalid configuration or definition (categories, rules, questions...).</summary>
public sealed class ConfigException(string message) : Exception(message);

/// <summary>The request cannot be honoured as stated (unknown category, bad answer...).</summary>
public sealed class InvalidRequestException(string message) : Exception(message);

/// <summary>The request conflicts with the current state (session already resolved...).</summary>
public sealed class ConflictException(string message) : Exception(message);

/// <summary>A referenced row does not exist.</summary>
public sealed class NotFoundException(string message = "not found") : Exception(message);
