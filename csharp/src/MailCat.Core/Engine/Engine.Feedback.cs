using MailCat.Categories;
using MailCat.Classification;
using MailCat.Explain;
using MailCat.Learning;
using MailCat.Storage;

namespace MailCat.Engine;

public sealed partial class MailCatEngine
{
    void CheckLabelable(string cat)
    {
        if (cat == CategoryTree.UnknownId) throw new InvalidRequestException($"\"{cat}\" is not a valid target category");
        if (!tree.Has(cat)) throw new InvalidRequestException($"unknown category \"{cat}\"");
        if (!tree.Enabled(cat)) throw new InvalidRequestException($"category \"{cat}\" is disabled");
    }

    /// <summary>
    /// Applies the user's verdict on the current classification (§16, §20, §33). correct=true
    /// confirms it; correct=false requires the right category. A user label has maximum
    /// weight: the email is final, with score 1.
    /// </summary>
    public Outcome Feedback(string emailId, bool correct, string category = "")
    {
        var st = LoadSession(emailId);
        if (correct)
        {
            var cat = st.Final;
            if (st.Decision == Decisions.Ask || cat == CategoryTree.UnknownId || cat == "")
                throw new InvalidRequestException("there is no classification to confirm yet");
            return Label(emailId, st, cat);
        }
        if (category == "") throw new InvalidRequestException("category is required when correct=false");
        CheckLabelable(category);
        return Label(emailId, st, category);
    }

    /// <summary>Files an email into a category by hand ("Move to...", §33).</summary>
    public Outcome Move(string emailId, string category)
    {
        var st = LoadSession(emailId);
        CheckLabelable(category);
        return Label(emailId, st, category);
    }

    /// <summary>Stores a user-established category.</summary>
    Outcome Label(string emailId, SessionState st, string cat)
    {
        var set = db.Features(emailId);
        var prev = st.Final;
        if (st.Decision == Decisions.Ask && st.Candidates.Count > 0) prev = st.Candidates[0].Category;

        string kind = InteractionKind.Manual;
        var lkind = FeedbackKind.Manual;
        if (prev == cat && st.Decision != Decisions.Ask)
        {
            kind = InteractionKind.Confirm;
            lkind = FeedbackKind.Confirm;
        }
        else if (prev != "" && prev != CategoryTree.UnknownId && prev != cat)
        {
            kind = InteractionKind.Correct;
            lkind = FeedbackKind.Correct;
            if (st.Decision == Decisions.Ask)
            {
                kind = InteractionKind.Manual; // nothing was committed yet
                lkind = FeedbackKind.Manual;
                prev = "";
            }
        }

        st.Decision = Decisions.User;
        st.Final = cat;
        st.FinalScore = 1.0;
        st.ResolvedBy = Resolved.User;
        st.Method = Method.User;
        st.Pending = null;
        Persist(emailId, st);
        db.AddInteraction(new Interaction
        {
            EmailId = emailId, SessionId = SessionIdOf(emailId), Kind = kind, PreviousPrediction = prev, ResultingCategory = cat,
        }, now());

        bool learned = Learn(emailId, set, cat, prev, lkind);
        var o = OutcomeOf(emailId, st);
        o.Learned = learned;
        return o;
    }
}
