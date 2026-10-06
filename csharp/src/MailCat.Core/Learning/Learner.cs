using MailCat.Categories;
using MailCat.Features;

namespace MailCat.Learning;

/// <summary>Kind of feedback.</summary>
public enum FeedbackKind
{
    /// <summary>The session was resolved thanks to the user's answers.</summary>
    Answer,
    /// <summary>The user confirmed an automatic classification.</summary>
    Confirm,
    /// <summary>The user said the prediction was wrong and chose another category.</summary>
    Correct,
    /// <summary>The user filed the email by hand ("Move to...", weight 1.0, §33).</summary>
    Manual,
}

/// <summary>One learning event.</summary>
public sealed record Feedback(string EmailId, FeatureSet Features, string Category, string Previous, FeedbackKind Kind);

/// <summary>Updates learned rules from feedback.</summary>
public sealed class Learner(ILearnedRuleRepo repo, CategoryTree tree, Func<DateTime>? now = null)
{
    DateTime Now() => now?.Invoke() ?? DateTime.UtcNow;

    /// <summary>
    /// Records the evidence of one feedback event. It only ever learns from user-provided
    /// signals; automatic classifications never reach this method, which prevents the
    /// system from reinforcing its own mistakes.
    /// </summary>
    public void Learn(Feedback fb)
    {
        if (fb.Category == "" || fb.Category == CategoryTree.UnknownId || fb.Features == null) return;
        if (!tree.Has(fb.Category)) throw new InvalidRequestException($"learn: unknown category \"{fb.Category}\"");
        var at = Now();
        var gens = Generalizations.Of(fb.Features);
        if (gens.Count == 0) return;

        var existing = repo.RulesByKeys(gens.Select(g => g.Key).ToList());

        // 1. rules that pointed somewhere unrelated to the true category were wrong
        foreach (var r in existing)
        {
            if (r.CategoryId == fb.Category || Related(r.CategoryId, fb.Category)) continue;
            if (r.Negative) continue; // negative rules stay valid
            r.Rejections++; // a positive rule claimed a different category
            Refresh(r, at);
            repo.SaveRule(r);
        }

        bool wrongPrev = fb.Previous != "" && fb.Previous != CategoryTree.UnknownId &&
                         fb.Previous != fb.Category && !Related(fb.Previous, fb.Category) &&
                         fb.Kind is FeedbackKind.Correct or FeedbackKind.Manual;

        foreach (var g in gens)
        {
            // 2. positive evidence for the established category
            Bump(fb.Category, g, negative: false, at);

            // 3. a negative rule that said "not this category" was contradicted
            var neg = repo.GetRule(fb.Category, g.Key, true);
            if (neg != null)
            {
                neg.Rejections++;
                Refresh(neg, at);
                repo.SaveRule(neg);
            }

            // 4. the wrong prediction becomes negative knowledge (§21), except for the most
            //    general level, which would over-penalize.
            if (wrongPrev && g.Kind != RuleKind.Intent) Bump(fb.Previous, g, negative: true, at);
        }
    }

    void Bump(string category, Generalization g, bool negative, DateTime at)
    {
        var r = repo.GetRule(category, g.Key, negative) ?? new LearnedRule
        {
            CategoryId = category, Key = g.Key, Kind = g.Kind, Conditions = [.. g.Conds], Negative = negative, CreatedAt = at,
        };
        r.Observations++;
        r.Confirmations++;
        Refresh(r, at);
        repo.SaveRule(r);
    }

    static void Refresh(LearnedRule r, DateTime at)
    {
        if (r.Kind == "") r.Kind = RuleKind.SenderDomain;
        r.Weight = Generalizations.RuleWeight(r);
        r.UpdatedAt = at;
    }

    /// <summary>
    /// One category is the ancestor of the other, so confirming one does not contradict the
    /// other (a rule for "finance" is not wrong when the user refines the email to "finance.bank").
    /// </summary>
    bool Related(string a, string b) => tree.IsAncestor(a, b) || tree.IsAncestor(b, a);
}
